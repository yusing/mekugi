package router

import (
	"reflect"
	"strings"
	"testing"
)

// Prefix each rejection with a valid allocation so rollback covers content,
// events, receipts, and sibling ordinals, not just the rejected operation.
func assertJournalDiagnosticRollback(t *testing.T, proxy *mekugiProxy, workspace string, mutation journalMutation, hints ...string) {
	t.Helper()
	before := treeSnapshot(t, proxy, workspace)
	_, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "tree", "rejected-diagnostic", []journalMutation{
		{Op: "add", Title: new("Must roll back")},
		mutation,
	})
	if err == nil {
		t.Fatal("invalid mutation succeeded")
	}
	for _, hint := range hints {
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("diagnostic %q omits %q", err, hint)
		}
	}
	if after := treeSnapshot(t, proxy, workspace); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected batch changed durable journal state")
	}
}

func TestJournalDiagnosticsRecoverStableTaskPaths(t *testing.T) {
	for _, path := range []string{"1", "/99"} {
		for _, op := range []string{"set", "remove", "log"} {
			t.Run(op+"/"+path, func(t *testing.T) {
				proxy, workspace := treeTestJournal(t)
				treeApply(t, proxy, workspace,
					journalMutation{Op: "add", Title: new("Root finding")},
					journalMutation{Op: "add", Kind: "task", Title: new("Parser")},
				)
				mutation := journalMutation{Op: op, P: path}
				if op == "set" {
					mutation.State = new("working")
				} else if op == "log" {
					mutation.Text = new("Parser fact")
				}
				hints := []string{"journal path not found: " + path, "read view tasks"}
				if path == "1" {
					hints = append(hints, "starting with /", "not a plan position")
				}
				assertJournalDiagnosticRollback(t, proxy, workspace, mutation, hints...)
				depth := 0
				nodes, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "tree", "", "", &depth, "tasks")
				if err != nil || len(nodes) != 1 || nodes[0].Path != "/2" || nodes[0].Kind != "task" {
					t.Fatalf("tasks-only recovery = %+v, %v", nodes, err)
				}
				treeApply(t, proxy, workspace, journalMutation{Op: "set", P: nodes[0].Path, State: new("working")})
				if got := treeApply(t, proxy, workspace, journalMutation{Op: "log", P: nodes[0].Path, Text: new("Recovered fact")}); !reflect.DeepEqual(got, []string{"/2/1"}) {
					t.Fatalf("corrected log paths = %v", got)
				}
				if got := treeApply(t, proxy, workspace, journalMutation{Op: "add", Title: new("Committed")}); !reflect.DeepEqual(got, []string{"/3"}) {
					t.Fatalf("rejection consumed sibling ordinal: %v", got)
				}
			})
		}
	}
}

func TestJournalDiagnosticsMountedAndBoundSubtreesRemainReadOnly(t *testing.T) {
	for _, path := range []string{"/1/1/@child/1", "/1/1", "/1"} {
		ops := []string{"remove"}
		if strings.Contains(path, "/@") {
			ops = []string{"set", "remove", "log"}
		}
		for _, op := range ops {
			t.Run(op+path, func(t *testing.T) {
				proxy, workspace := mountFixture(t)
				treeApply(t, proxy, workspace,
					journalMutation{Op: "add", Kind: "task", Title: new("Parent")},
					journalMutation{Op: "add", Under: "/1", Kind: "task", Title: new("Delegated"), Agent: "/root/child"},
				)
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "child", "", []journalMutation{{Op: "log", Text: new("Child fact")}}); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(path, "/@") {
					if _, ok := mountFind(mountRead(t, proxy, workspace, "tree", "", ""), path); !ok {
						t.Fatalf("fixture mount missing: %s", path)
					}
				}
				childBefore, exists, err := readThreadJournal(proxy.replayStore, workspace, "child")
				if err != nil || !exists {
					t.Fatalf("child journal = %v, %v", exists, err)
				}
				mutation := journalMutation{Op: op, P: path}
				if op == "set" {
					mutation.Title = new("Tampered")
				} else if op == "log" {
					mutation.Text = new("Tampered")
				}
				hints := []string{path, "immutable agent binding", "update the owned task"}
				if strings.Contains(path, "/@") {
					hints = []string{path, "read-only mounted view", "record a note or update your owned task"}
				}
				assertJournalDiagnosticRollback(t, proxy, workspace, mutation, hints...)
				childAfter, _, err := readThreadJournal(proxy.replayStore, workspace, "child")
				if err != nil || !reflect.DeepEqual(childBefore, childAfter) {
					t.Fatalf("rejection changed child journal: %v", err)
				}
				treeApply(t, proxy, workspace, journalMutation{Op: "set", P: "/1/1", State: new("working")})
				if got := treeApply(t, proxy, workspace, journalMutation{Op: "log", P: "/1/1", Text: new("Parent integration finding")}); !reflect.DeepEqual(got, []string{"/1/1/1"}) {
					t.Fatalf("owned correction = %v", got)
				}
			})
		}
	}
}

func TestJournalDiagnosticsRouterOwnedAnswerCannotBeChanged(t *testing.T) {
	for _, op := range []string{"set", "remove"} {
		t.Run(op, func(t *testing.T) {
			proxy, workspace := treeTestJournal(t)
			treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Parser")})
			transform, _ := prepareActivityTest(t, proxy, "tree", "tree", "", "/root", nil)
			if err := transform.captureNaturalJournalAnswer(regressionFinalResponse(t, "diagnostic-answer", "Parser implemented.")); err != nil {
				t.Fatal(err)
			}
			nodes, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "tree", "", "", nil, "own")
			if err != nil || len(nodes) != 2 || nodes[1].Kind != "answer" {
				t.Fatalf("captured answer = %+v, %v", nodes, err)
			}
			answer := nodes[1]
			mutation := journalMutation{Op: op, P: answer.Path}
			if op == "set" {
				mutation.Body = new("Rewritten outcome")
			}
			assertJournalDiagnosticRollback(t, proxy, workspace, mutation,
				answer.Path, "router-owned journal node is read-only", answer.Kind, answer.Title,
				"record a note or update your owned task")
			treeApply(t, proxy, workspace,
				journalMutation{Op: "set", P: "/1", State: new("working")},
				journalMutation{Op: "log", P: "/1", Text: new("Outcome correction: parser still needs validation")},
			)
			corrected, err := proxy.journals.readTree(t.Context(), proxy.replayStore, workspace, "tree", "", "", nil, "own")
			if err != nil || len(corrected) != 2 || !reflect.DeepEqual(corrected[1], answer) || len(corrected[0].Children) != 1 || corrected[0].State != "working" {
				t.Fatalf("correction changed captured outcome or lost owned update: %+v, %v", corrected, err)
			}
		})
	}
}

func TestJournalDiagnosticsNonTaskStateIdentifiesCorrection(t *testing.T) {
	for _, kind := range []string{"note", "context"} {
		for _, under := range []string{"", "/1"} {
			for _, field := range []string{"state", "reason"} {
				t.Run(kind+under+"/"+field, func(t *testing.T) {
					proxy, workspace := treeTestJournal(t)
					treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Parser")})
					paths := treeApply(t, proxy, workspace, journalMutation{Op: "add", Under: under, Kind: kind, Title: new("Finding")})
					mutation := journalMutation{Op: "set", P: paths[0]}
					if field == "state" {
						mutation.State = new("working")
					} else {
						mutation.Reason = new("Waiting for input")
					}
					hints := []string{paths[0], "only tasks have state or reason", kind, "Finding", "edit this node's title/body"}
					if under != "" {
						hints = append(hints, "set the containing task /1 instead")
					} else {
						hints = append(hints, "create kind task for work with a state")
					}
					assertJournalDiagnosticRollback(t, proxy, workspace, mutation, hints...)
					treeApply(t, proxy, workspace,
						journalMutation{Op: "set", P: "/1", State: new("blocked"), Reason: new("Waiting for input")},
						journalMutation{Op: "set", P: paths[0], Body: new("Input dependency recorded")},
					)
					journal := treeSnapshot(t, proxy, workspace)
					if journal.Items[0].State != "blocked" || journal.Items[0].Reason != "Waiting for input" || journal.Items[1].State != "" || journal.Items[1].Reason != "" || journal.Items[1].Body != "Input dependency recorded" {
						t.Fatalf("corrected task/note state = %+v", journal.Items)
					}
				})
			}
		}
	}
}
