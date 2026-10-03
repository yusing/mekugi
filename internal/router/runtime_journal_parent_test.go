package router

import (
	"net/http"
	"strings"
	"testing"
)

func nativeParentReceipt(t *testing.T, s *ObservationService, c *http.Client, b ObservationBinding, id string) {
	t.Helper()
	call := ObservationCall{Binding: b, ID: id, Tool: "Agent", Input: `{"prompt":"native child"}`}
	if status, body := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "journal_before", Call: call}); status != http.StatusOK {
		t.Fatalf("parent receipt: %d %s", status, body)
	}
}

func TestRuntimeJournalNativeParentProofAndLifecycleReplay(t *testing.T) {
	s, root, c := runtimeJournalFixture(t)
	child, grandchild := root, root
	child.Agent = "child"
	grandchild.Agent = "grandchild"
	runtimeJournalBind(t, s, child, c)
	runtimeJournalBind(t, s, grandchild, c)
	runtimeJournalAdd(t, s, child, c, "child-task", "Child task")
	runtimeJournalAdd(t, s, grandchild, c, "grandchild-task", "Grandchild task")
	nativeParentReceipt(t, s, c, root, "spawn-child")
	nativeParentReceipt(t, s, c, child, "spawn-grandchild")
	if err := s.journal.parent(t.Context(), "spawn-grandchild", grandchild.Agent, "completed"); err != nil {
		t.Fatal(err)
	}
	if nodes := runtimeJournalRead(t, s, root, c, "unbound", `{}`); len(nodes) != 0 {
		t.Fatal("unproven parent mounted child")
	}
	if err := s.journal.parent(t.Context(), "spawn-child", child.Agent, "completed"); err != nil {
		t.Fatal(err)
	}
	children, err := s.journal.journals.listAgent(t.Context(), s.owner.store, root.Workspace, observationThread(root), "/root/child/grandchild")
	if err != nil || len(children) != 1 || children[0].Title != "Grandchild task" {
		t.Fatalf("nested ancestry not authorized: %+v %v", children, err)
	}
	runtimeJournalAdd(t, s, child, c, "new-child-work", "New work")
	if err := s.journal.parent(t.Context(), "spawn-child", child.Agent, "completed"); err != nil {
		t.Fatal(err)
	}
	ctx, err := s.journal.scope(t.Context(), child)
	if err != nil {
		t.Fatal(err)
	}
	j, ok, err := readThreadJournal(s.owner.store.scoped(ctx), root.Workspace, observationThread(child))
	if err != nil || !ok || j.LifecycleState != "working" || j.Items[0].State != "working" {
		t.Fatalf("replayed completion revived old child lifecycle: %+v %v", j, err)
	}
	if err := s.journal.parent(t.Context(), "child-task", child.Agent, "completed"); err == nil {
		t.Fatal("MCP tool authorized native parent")
	}
	mount := `{"journal":[{"op":"add","kind":"task","title":"Integrate child","agent":"/root/child"}]}`
	runtimeJournalReceipt(t, s, root, c, "mount", "journal_batch", mount)
	runtimeJournalInvoke(t, s, c, "mount", "journal_batch", mount)
	nodes := runtimeJournalRead(t, s, root, c, "mounted", `{}`)
	if len(nodes) != 1 || len(nodes[0].Children) == 0 {
		t.Fatalf("verified child missing shared mount: %+v", nodes)
	}
}

func TestRuntimeJournalRecoveryUsesNativeScopeAndMandatoryOverflow(t *testing.T) {
	s, b, c := runtimeJournalFixture(t)
	runtimeJournalAdd(t, s, b, c, "working", "Keep task")
	text, err := s.journal.recover(t.Context(), b)
	if err != nil || journalSummaryCharacters(text) > 10000 {
		t.Fatalf("recovery: %v", err)
	}
	alien := b
	alien.Session = "alien"
	if _, err := s.journal.recover(t.Context(), alien); err == nil {
		t.Fatal("recovery crossed native session")
	}
	child := b
	child.Agent = "unbound"
	runtimeJournalBind(t, s, child, c)
	if _, err := s.journal.recover(t.Context(), child); err == nil {
		t.Fatal("unbound child context manufactured ancestry")
	}
	ctx, err := s.journal.scope(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("x", 10001)
	if _, err := s.journal.journals.apply(ctx, s.owner.store, b.Workspace, observationThread(b), "overflow", []journalMutation{{Op: "add", Kind: "context", Title: new("Mandatory"), Body: new(string(body))}}); err != nil {
		t.Fatal(err)
	}
	if text, err := s.journal.recover(t.Context(), b); err == nil || text != "" {
		t.Fatal("mandatory recovery overflow exposed truncated facts")
	}
}

func TestRuntimeJournalBackgroundTaskLifecycle(t *testing.T) {
	for _, status := range []string{"completed", "failed", "stopped"} {
		for _, early := range []bool{false, true} {
			t.Run(status+"/"+map[bool]string{false: "late", true: "early"}[early], func(t *testing.T) {
				s, root, c := runtimeJournalFixture(t)
				child := root
				child.Agent = "native-child"
				runtimeJournalBind(t, s, child, c)
				nativeParentReceipt(t, s, c, root, "spawn-background")
				start := observationTask{ID: "distinct-task-id", CallID: "spawn-background", Session: root.Session}
				if err := s.journal.task(t.Context(), start, true); err != nil {
					t.Fatal(err)
				}
				terminal := start
				terminal.CallID, terminal.Status = "", status
				settle := func() {
					t.Helper()
					if code, body := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "task", Task: terminal}); code != http.StatusOK {
						t.Fatalf("terminal task: %d %s", code, body)
					}
				}
				if early {
					settle()
				}
				runtimeJournalAdd(t, s, child, c, "background-work", "Still authored working")
				if err := s.journal.parent(t.Context(), "spawn-background", child.Agent, "async_launched"); err != nil {
					t.Fatal(err)
				}
				if !early {
					settle()
				}
				ctx, err := s.journal.scope(t.Context(), child)
				if err != nil {
					t.Fatal(err)
				}
				j, _, err := readThreadJournal(s.owner.store.scoped(ctx), root.Workspace, observationThread(child))
				want := "blocked"
				if status == "completed" {
					want = "done"
				}
				if err != nil || j.LifecycleState != want || j.Items[0].State != "working" {
					t.Fatalf("native completion lost or authored state changed: %+v %v", j, err)
				}
				// A replayed start/terminal cannot overwrite subsequent child work.
				runtimeJournalAdd(t, s, child, c, "new-work", "Later native work")
				if err := s.journal.task(t.Context(), start, true); err != nil {
					t.Fatal(err)
				}
				settle()
				j, _, err = readThreadJournal(s.owner.store.scoped(ctx), root.Workspace, observationThread(child))
				if err != nil || j.LifecycleState != "working" {
					t.Fatalf("duplicate terminal replayed lifecycle: %+v %v", j, err)
				}
				mismatch := terminal
				mismatch.CallID = "alien-call"
				if err := s.journal.task(t.Context(), mismatch, false); err == nil {
					t.Fatal("terminal with changed native call accepted")
				}
			})
		}
	}
}

func TestRuntimeJournalNativeIdentityBeforeSubagentStart(t *testing.T) {
	s, root, c := runtimeJournalFixture(t)
	nativeParentReceipt(t, s, c, root, "spawn-early")
	child := root
	child.Agent = "early-child"
	if err := s.journal.parent(t.Context(), "spawn-early", child.Agent, "async_launched"); err != nil {
		t.Fatal(err)
	}
	// Native MCP hook identity arrives before any SubagentStart callback.
	runtimeJournalAdd(t, s, child, c, "early-mutation", "Child owns its journal")
	runtimeJournalBind(t, s, child, c)
	nodes, err := s.journal.journals.listAgent(t.Context(), s.owner.store, root.Workspace, observationThread(root), "/root/early-child")
	if err != nil || len(nodes) != 1 || nodes[0].Title != "Child owns its journal" {
		t.Fatalf("native identity delivery ordering lost child scope: %+v %v", nodes, err)
	}
	alien := child
	alien.Session = "alien"
	if err := s.journal.before(t.Context(), ObservationCall{Binding: alien, ID: "alien-call", Tool: "mcp__mekugi__journal_read", Input: `{}`}); err == nil {
		t.Fatal("native hook admitted an unrelated session")
	}
}

func TestRuntimeJournalContinuedChildPreservesImmutableAncestry(t *testing.T) {
	s, root, c := runtimeJournalFixture(t)
	child := root
	child.Agent = "continued-child"
	nativeParentReceipt(t, s, c, root, "initial-spawn")
	runtimeJournalAdd(t, s, child, c, "initial-work", "Initial work")
	if err := s.journal.parent(t.Context(), "initial-spawn", child.Agent, "completed"); err != nil {
		t.Fatal(err)
	}
	nativeParentReceipt(t, s, c, root, "continued-call")
	runtimeJournalAdd(t, s, child, c, "continued-work", "Later work")
	if err := s.journal.parent(t.Context(), "continued-call", child.Agent, "async_launched"); err != nil {
		t.Fatal(err)
	}
	start := observationTask{ID: "continued-task", CallID: "continued-call", Session: root.Session}
	if err := s.journal.task(t.Context(), start, true); err != nil {
		t.Fatal(err)
	}
	start.Status = "completed"
	if err := s.journal.task(t.Context(), start, false); err != nil {
		t.Fatal(err)
	}
	ctx, err := s.journal.scope(t.Context(), child)
	if err != nil {
		t.Fatal(err)
	}
	proof, found, err := s.owner.store.lookup(ctx, root.Workspace, observationThread(child)+"/journal-parent")
	if err != nil || !found || proof.NativeObservation.Call.ID != "initial-spawn" {
		t.Fatalf("continued call overwrote immutable ancestry proof: %+v %v", proof, err)
	}
	j, found, err := readThreadJournal(s.owner.store.scoped(ctx), root.Workspace, observationThread(child))
	if err != nil || !found || j.Parent != observationThread(root) || j.LifecycleState != "done" || len(j.Items) != 2 || j.Items[0].State != "working" || j.Items[1].State != "working" {
		t.Fatalf("continued native lifecycle lost or authored tasks changed: %+v %v", j, err)
	}
}

func TestRuntimeJournalMixedTerminalCarriersDoNotReplayLifecycle(t *testing.T) {
	for _, taskFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "result-first", true: "task-first"}[taskFirst], func(t *testing.T) {
			s, root, c := runtimeJournalFixture(t)
			child := root
			child.Agent = "mixed-child"
			nativeParentReceipt(t, s, c, root, "spawn-mixed")
			runtimeJournalAdd(t, s, child, c, "initial-work", "Initial work")
			start := observationTask{ID: "mixed-task", CallID: "spawn-mixed", Session: root.Session}
			if err := s.journal.task(t.Context(), start, true); err != nil {
				t.Fatal(err)
			}
			terminal := start
			terminal.Status = "completed"
			if taskFirst {
				if err := s.journal.parent(t.Context(), "spawn-mixed", child.Agent, "async_launched"); err != nil {
					t.Fatal(err)
				}
				if err := s.journal.task(t.Context(), terminal, false); err != nil {
					t.Fatal(err)
				}
			} else if err := s.journal.parent(t.Context(), "spawn-mixed", child.Agent, "completed"); err != nil {
				t.Fatal(err)
			}
			runtimeJournalAdd(t, s, child, c, "later-work", "Later native work")
			if err := s.journal.task(t.Context(), terminal, false); err != nil {
				t.Fatal(err)
			}
			ctx, err := s.journal.scope(t.Context(), child)
			if err != nil {
				t.Fatal(err)
			}
			j, _, err := readThreadJournal(s.owner.store.scoped(ctx), root.Workspace, observationThread(child))
			if err != nil || j.LifecycleState != "working" || j.Items[1].State != "working" {
				t.Fatalf("old cross-carrier terminal overwrote later work: %+v %v", j, err)
			}
		})
	}
}
