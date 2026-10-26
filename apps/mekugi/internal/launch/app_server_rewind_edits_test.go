package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func rewindEditsFixture(t *testing.T) (*appServerUI, *appServerTestInput) {
	t.Helper()
	u, w := newResumeSessionTestUI(t)
	u.clock = func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local) }
	workspace := t.TempDir()
	u.startSession("main", workspace)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, thread := range []string{"main", "child"} {
		path := filepath.Join(t.TempDir(), thread+".jsonl")
		sources[thread] = path
		text := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q}}`, thread) + "\n"
		for i, turn := range []string{"first", "second"} {
			stamp := fmt.Sprintf("2026-10-21T01:00:0%dZ", i)
			call := thread + "-" + turn
			text += fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"task_started","turn_id":%q}}`, stamp, turn) + "\n"
			text += fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"function_call","call_id":%q,"name":"exec_command","arguments":"{}"}}`, stamp, call) + "\n"
			text += fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"function_call_output","call_id":%q,"output":"ok"}}`, stamp, call) + "\n"
			liveDiffScopeCapture(t, store, workspace, thread, call, filepath.Join(workspace, call+".txt"), "before", "after")
		}
		writeTestFile(t, path, text)
	}
	scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true, "child": true}}}
	snapshot, err := store.liveDiffSnapshot(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	u.review.Projection = sessionreview.Projection{Changes: store.changes, Retained: store.changeScope(), Data: snapshot, Scope: u.review.Scope()}
	rewindTestUser(t, u)
	appServerTestKeys(t, u, "/rewind\r")
	read := resumeTestOne(t, w, "thread/read")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","path":%q,"turns":[{"id":"first","status":"completed","items":[{"type":"userMessage","content":[{"type":"text","text":"Implement the parser"}]}]},{"id":"second","status":"completed","items":[{"type":"userMessage","content":[{"type":"text","text":"Change the error format"}]}]}]}}}`, read.ID, sources["main"]))
	appServerTestKeys(t, u, "\x1b[B\r")
	child := resumeTestOne(t, w, "thread/read")
	if child.Params.ThreadID != "child" {
		t.Fatalf("source read: %+v", child)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"child","path":%q}}}`, child.ID, sources["child"]))
	select {
	case result := <-u.picker.scanResults:
		if !u.picker.applyScan(u.attached.cwd, u.picker.Target, result) {
			t.Fatal("plan result not admitted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rewind plan did not finish")
	}
	if len(u.picker.Choices) != 2 || u.picker.Choices[1].Name != "Revert 2 captured changes" || strings.Count(u.picker.rewind.command, "mchanges revert") != 1 {
		t.Fatalf("revert plan: %+v", u.picker.rewind)
	}
	return u, w
}

func TestUISnapshotNativeRewindEdits(t *testing.T) {
	for _, width := range []int{48, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := rewindEditsFixture(t)
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			u.clock = func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local) }
			rows, _ := u.testMainFrame(width, 18, 0)
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-rewind-edits-%d.txt", width), rows, width)
		})
	}
}

func TestAppServerRewindRevertGates(t *testing.T) {
	for _, outcome := range []string{"success", "late-ack", "conflict", "rejected", "cancel", "keep"} {
		t.Run(outcome, func(t *testing.T) {
			u, w := rewindEditsFixture(t)
			u.reasoningEffort = "high"
			if outcome == "cancel" || outcome == "keep" {
				if outcome == "keep" {
					appServerTestKeys(t, u, "\r")
				} else {
					draft := u.Text
					composerShellEscape(t, u)
					if u.picker.Open || u.picker.Kind != "" || u.Text != draft {
						t.Fatal("cancel did not close the dialog and preserve its draft")
					}
				}
				if outcome == "keep" {
					resumeTestOne(t, w, "thread/fork")
				}
				if w.Len() != 0 || u.Rewind != nil {
					t.Fatal("keep/cancel dispatched a reversal")
				}
				return
			}
			command := u.picker.rewind.command
			appServerTestKeys(t, u, "\x1b[B\r")
			requests := appServerDrainRequests[struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
				Params struct {
					Command string `json:"command"`
					Thread  string `json:"threadId"`
				} `json:"params"`
			}](t, w)
			if len(requests) != 1 || requests[0].Method != "thread/shellCommand" || requests[0].Params.Command != command || requests[0].Params.Thread != "main" {
				t.Fatalf("reversal: %+v", requests)
			}
			appServerTestKeys(t, u, "preserved draft")
			if outcome == "rejected" {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"denied"}}`, requests[0].ID))
			} else {
				ack := fmt.Sprintf(`{"id":%d,"result":{}}`, requests[0].ID)
				if outcome != "late-ack" {
					appServerTestMessage(t, u, ack)
				}
				appServerTestTurn(t, u, "shell")
				appServerTestMessage(t, u, `{"method":"item/started","params":{"threadId":"main","turnId":"shell","item":{"type":"commandExecution","id":"revert","source":"userShell","status":"inProgress"}}}`)
				exit := 0
				if outcome == "conflict" {
					exit = 1
				}
				appServerTestMessage(t, u, fmt.Sprintf(`{"method":"item/completed","params":{"threadId":"main","turnId":"shell","item":{"type":"commandExecution","id":"revert","source":"userShell","status":"completed","exitCode":%d}}}`, exit))
				if w.Len() != 0 {
					t.Fatal("fork preceded shell turn completion")
				}
				appServerTestTurnEnd(t, u, "shell", "completed")
				if outcome == "late-ack" {
					if w.Len() != 0 {
						t.Fatal("fork preceded shell acknowledgement")
					}
					appServerTestMessage(t, u, ack)
				}
			}
			if outcome == "success" || outcome == "late-ack" {
				fork := resumeTestOne(t, w, "thread/fork")
				if fork.Params.BeforeTurnID != "second" {
					t.Fatalf("fork boundary: %+v", fork)
				}
			} else if w.Len() != 0 || u.Replacement.Pending() || u.Rewind != nil {
				t.Fatal("failed reversal forked or kept a pending replacement")
			}
			if u.Thread != "main" || u.Text != "preserved draft" {
				t.Fatal("reversal lost original session or draft")
			}
		})
	}
}

func TestAppServerRewindRejectsStalePlan(t *testing.T) {
	for _, change := range []string{"capture", "membership", "failure"} {
		t.Run(change, func(t *testing.T) {
			u, w := rewindEditsFixture(t)
			switch change {
			case "capture":
				builder := changepresentation.NewBuilder(u.review.Projection.Data)
				for attempt := range u.review.Projection.Data.Attempts() {
					if attempt.Operation.Thread != "child" {
						continue
					}
					attempt.Key.Call, attempt.Change, attempt.Operation.Item = "child-later", "amber99", "child-later"
					if err := builder.Add(attempt); err != nil {
						t.Fatal(err)
					}
					break
				}
				u.review.Projection.Data = builder.Finish()
			case "membership":
				u.review.IncludeThread(u.attached.cwd, "late-child", false)
			case "failure":
				u.review.Failure = "retained evidence unavailable"
			}
			appServerTestKeys(t, u, "\x1b[B\r")
			if w.Len() != 0 || u.Rewind != nil || len(u.picker.Choices) != 1 || u.picker.rewind.problem == "" {
				t.Fatal("stale plan dispatched or remained selectable")
			}
			appServerTestKeys(t, u, "\r")
			resumeTestOne(t, w, "thread/fork")
		})
	}
}

func TestAppServerRewindTurnEndsBeforeItem(t *testing.T) {
	for _, outcome := range []string{"failed", "interrupted"} {
		t.Run(outcome, func(t *testing.T) {
			u, w := rewindEditsFixture(t)
			appServerTestKeys(t, u, "\x1b[B\r")
			request := resumeTestOne(t, w, "thread/shellCommand")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
			appServerTestTurn(t, u, "shell")
			if outcome == "interrupted" {
				nativeTestCancel(t, u)
				interrupt := resumeTestOne(t, w, "turn/interrupt")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
			} else {
				appServerTestMessage(t, u, `{"method":"error","params":{"threadId":"main","turnId":"shell","error":{"message":"shell unavailable"}}}`)
			}
			appServerTestTurnEnd(t, u, "shell", outcome)
			if u.Rewind != nil || u.Replacement.Pending() || w.Len() != 0 || u.Thread != "main" {
				t.Fatal("terminal shell failure blocked the session or forked")
			}
			appServerTestKeys(t, u, "next request\r")
			resumeTestOne(t, w, "turn/start")
		})
	}
}

func TestAppServerRewindFailureRetiresPendingCapture(t *testing.T) {
	for _, outcome := range []string{"failed", "interrupted"} {
		t.Run(outcome, func(t *testing.T) {
			u := newShellEditTestUI(t)
			w := u.Host.Input.(*appServerTestInput)
			if err := dispatchShell(&u.attached, u.shell, &u.Client, u.clock, u.ctx, u.review, &u.shellEdits, u.retained, u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane, "printf after > captured.txt"); err != nil {
				t.Fatal(err)
			}
			if u.shellEdits.pending == nil {
				t.Fatal("fixture did not create capture")
			}
			u.Rewind = &sessioncontrol.RewindOperation{Thread: u.Thread, Sent: true, BeforeTurn: "old"}
			u.Replacement.Rewind()
			request := resumeTestOne(t, w, "thread/shellCommand")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
			appServerTestTurn(t, u, "shell")
			appServerTestTurnEnd(t, u, "unrelated", outcome)
			if u.shellEdits.pending == nil {
				t.Fatal("unrelated turn discarded the pending capture")
			}
			appServerTestTurnEnd(t, u, "shell", outcome)
			if u.Rewind != nil || u.Replacement.Pending() || u.shellEdits.pending != nil {
				t.Fatal("terminal failure retained rewind or its unbound capture")
			}
		})
	}
}

func TestUISnapshotNativeRewindNoEdits(t *testing.T) {
	for _, scenario := range []string{"empty", "before", "cancel", "fork-failure"} {
		t.Run(scenario, func(t *testing.T) {
			u, w := rewindEditsFixture(t)
			sources := u.picker.rewind.sources
			composerShellEscape(t, u)
			if scenario == "empty" {
				u.review.Projection.Data = changepresentation.NewBuilder(nil).Finish()
			}
			text, err := os.ReadFile(sources[0].Path)
			if err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, sources[0].Path, string(text)+`{"timestamp":"2026-10-21T01:00:03Z","type":"event_msg","payload":{"type":"task_started","turn_id":"third"}}`+"\n")
			appServerTestKeys(t, u, "/rewind\r")
			read := resumeTestOne(t, w, "thread/read")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","path":%q,"turns":[{"id":"third","status":"completed","items":[{"type":"userMessage","content":[{"type":"text","text":"Explain the changes"}]}]}]}}}`, read.ID, sources[0].Path))
			appServerTestKeys(t, u, "\r")
			if scenario != "empty" {
				for _, width := range []int{48, 100} {
					rows, _ := u.testMainFrame(width, 18, 0)
					uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-rewind-checking-%d.txt", width), rows, width)
				}
				read = resumeTestOne(t, w, "thread/read")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"child","path":%q}}}`, read.ID, sources[1].Path))
				select {
				case result := <-u.picker.scanResults:
					if scenario == "cancel" {
						composerShellEscape(t, u)
					}
					u.picker.applyScan(u.attached.cwd, u.picker.Target, result)
				case <-time.After(5 * time.Second):
					t.Fatal("rewind plan did not finish")
				}
				u.testMainFrame(100, 18, 0)
			}
			if scenario != "cancel" {
				fork := resumeTestOne(t, w, "thread/fork")
				if fork.Params.BeforeTurnID != "third" {
					t.Fatalf("fork boundary: %+v", fork)
				}
				if scenario == "fork-failure" {
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"fork failed"}}`, fork.ID))
					u.testMainFrame(100, 18, 0)
					if u.Replacement.Pending() || u.Thread != "main" {
						t.Fatal("failed empty-selection fork blocked the original session")
					}
				}
			}
			if w.Len() != 0 || u.picker.Kind == "rewind-edits" || u.Rewind != nil {
				t.Fatal("empty selection prompted for edits or dispatched a reversal")
			}
		})
	}
}

func TestRewindIgnoresDiagnosticOnlyChildren(t *testing.T) {
	for _, authored := range []bool{false, true} {
		t.Run(fmt.Sprint(authored), func(t *testing.T) {
			u, w := newResumeSessionTestUI(t)
			workspace := t.TempDir()
			u.startSession("main", workspace)
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			builder := changepresentation.NewBuilder(nil)
			diagnostic := changepresentation.Attempt{Key: changepresentation.AttemptKey{Workspace: workspace, Call: "git"}, Change: "amber1", Operation: activitydetail.Operation{Thread: "diagnostic-child", Item: "git"}, Chunks: []changepresentation.Chunk{{Review: diffmodel.ReviewFile{AfterPath: filepath.Join(workspace, "imported"), Origin: "git reset"}}}}
			if err := builder.Add(diagnostic); err != nil {
				t.Fatal(err)
			}
			if authored {
				edit := diagnostic
				edit.Key.Call, edit.Change, edit.Operation = "edit", "amber2", activitydetail.Operation{Thread: "main", Item: "edit"}
				edit.Chunks = []changepresentation.Chunk{{Review: diffmodel.ReviewFile{AfterPath: filepath.Join(workspace, "authored")}}}
				if err := builder.Add(edit); err != nil {
					t.Fatal(err)
				}
			}
			u.review.Projection = sessionreview.Projection{Changes: store.changes, Retained: store.changeScope(), Data: builder.Finish(), Scope: u.review.Scope()}
			rewindTestUser(t, u)
			appServerTestKeys(t, u, "/rewind\r")
			read := resumeTestOne(t, w, "thread/read")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","path":"/fixture/main.jsonl","turns":[{"id":"first","status":"completed","items":[{"type":"userMessage","content":[{"type":"text","text":"Task"}]}]}]}}}`, read.ID))
			appServerTestKeys(t, u, "\r")
			if authored {
				if requests := resumeTestRequests(t, w); len(requests) != 0 || len(u.picker.rewind.pending) != 0 || u.picker.Kind != "rewind-edits" {
					t.Fatalf("diagnostic child acquired authored history: requests=%+v pending=%v menu=%s", requests, u.picker.rewind.pending, u.picker.Kind)
				}
			} else {
				if u.picker.Kind == "rewind-edits" {
					t.Fatalf("diagnostic-only capture triggered edit selection: %+v", u.picker.Choices)
				}
				resumeTestOne(t, w, "thread/fork")
			}
		})
	}
}
