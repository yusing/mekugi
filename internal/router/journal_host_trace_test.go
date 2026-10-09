package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"os"
	"reflect"
	"testing"
	"testing/synctest"
)

func traceChildResult(f *nativeTraceFixture, thread, parent, turn, agent string, status any) {
	f.t.Helper()
	f.event(thread, map[string]any{"type": "agent_result_observed", "child_thread_id": thread, "parent_thread_id": parent,
		"child_codex_turn_id": turn, "carried_payload": f.ref(map[string]any{"child_agent_path": agent, "status": status})})
}

func TestJournalTraceChildLifecycleAtConsumer(t *testing.T) {
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Integrate"), State: new("working"), Agent: "/root/child"})
	applyChildDeltaItems(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Unfinished"), State: new("working")})
	f := newNativeTraceFixture(t)
	proxy.nativeTrace = &nativeToolTrace{directory: f.root}
	read := func(want string) {
		t.Helper()
		nodes, err := proxy.commentary.journalReader(t.Context(), workspace+"\x00tree", "tree", "", "", nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if mount, ok := mountFind(nodes, "/1/@child"); !ok || mount.State != want {
			t.Fatalf("mount = %+v, want %s", mount, want)
		}
	}
	if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, "child", "first"); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "working", ""); err != nil {
		t.Fatal(err)
	}
	f.event("child", map[string]any{"type": "codex_turn_started", "codex_turn_id": "first"})
	read("working")
	closeTask := []journalMutation{{Op: "set", P: "/1", State: new("done")}}
	if _, err := proxy.commentary.journalPublisher(t.Context(), workspace+"\x00tree", "tree", "running", closeTask); err == nil {
		t.Fatal("running child permitted parent completion")
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "done", ""); err != nil {
		t.Fatal(err)
	}
	read("done") // Incomplete trace must preserve an app-server host outcome.
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "working", ""); err != nil {
		t.Fatal(err)
	}
	before, _, _ := readThreadJournal(proxy.replayStore, workspace, "child")
	traceChildResult(f, "child", "tree", "first", "/root/child", map[string]any{"completed": "host result"})
	if _, err := proxy.commentary.journalReader(t.Context(), workspace+"\x00sibling", "sibling", "", "", nil, ""); err != nil {
		t.Fatal(err)
	}
	if child, _, _ := readThreadJournal(proxy.replayStore, workspace, "child"); child.LifecycleState != "working" {
		t.Fatal("sibling request changed child lifecycle")
	}
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	transform.directory, transform.shellThreadID = workspace, "tree"
	result, err := transform.executeJournalCall(map[string]jsonv1.RawMessage{
		"name": mustMarshalJSON("journal"), "call_id": mustMarshalJSON("native-read"), "arguments": mustMarshalJSON(`{"op":"read"}`),
	})
	var nativeRead struct {
		Items []journalNode `json:"items"`
	}
	if err != nil || json.Unmarshal([]byte(jsonString(result, "output")), &nativeRead) != nil {
		t.Fatalf("native read failed: %+v, %v", result, err)
	}
	if mount, ok := mountFind(nativeRead.Items, "/1/@child"); !ok || mount.State != "done" {
		t.Fatalf("native read did not refresh child: %+v", nativeRead)
	}
	read("done")
	// Host starts precede model requests. Old completion must not close a follow-up.
	f.event("child", map[string]any{"type": "codex_turn_started", "codex_turn_id": "follow-up"})
	traceChildResult(f, "child", "tree", "first", "/root/child", map[string]any{"completed": "late old result"})
	read("working")
	if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, "child", "follow-up"); err != nil {
		t.Fatal(err)
	}
	// Mutation-time refresh catches a result published after the parent's read.
	traceChildResult(f, "child", "tree", "follow-up", "/root/child", map[string]any{"completed": nil})
	if _, err := proxy.commentary.journalPublisher(t.Context(), workspace+"\x00tree", "tree", "complete", closeTask); err != nil {
		t.Fatal(err)
	}
	proxy.journals = newJournalStore()
	proxy.nativeTrace = nil
	read("done")
	after, _, err := readThreadJournal(proxy.replayStore, workspace, "child")
	if err != nil || !reflect.DeepEqual(before.Events, after.Events) || after.Items[0].State != "working" {
		t.Fatalf("host evidence changed child-authored work: %+v, %v", after, err)
	}
	// A fresh invocation can observe a follow-up that fails before any model request.
	treeApply(t, proxy, workspace, journalMutation{Op: "set", P: "/1", State: new("working")})
	f = newNativeTraceFixture(t)
	proxy.nativeTrace = &nativeToolTrace{directory: f.root}
	f.event("child", map[string]any{"type": "codex_turn_started", "codex_turn_id": "interrupted-follow-up"})
	traceChildResult(f, "child", "tree", "interrupted-follow-up", "/root/child", "interrupted")
	read("blocked")
	if _, err := proxy.commentary.journalPublisher(t.Context(), workspace+"\x00tree", "tree", "interrupted", closeTask); err == nil {
		t.Fatal("interrupted follow-up permitted parent completion")
	}
	if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, "child", "live-follow-up"); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "working", ""); err != nil {
		t.Fatal(err)
	}
	read("working") // An unmatched older failure cannot replace a live turn.
}

func TestJournalTraceFreshTraceSettlesObservedFollowUp(t *testing.T) {
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Integrate"), State: new("working"), Agent: "/root/child"})
	if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, "child", "before-restart"); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "done", ""); err != nil {
		t.Fatal(err)
	}
	ctx, release, err := proxy.replayStore.beginSession(t.Context(), "tree", "tree")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// A restarted router's trace has no record of the durable turn.
	f := newNativeTraceFixture(t)
	proxy.nativeTrace = &nativeToolTrace{directory: f.root}
	refresh := func(want string, turns int) {
		t.Helper()
		if err := proxy.refreshJournalChildLifecycles(ctx, workspace, "tree"); err != nil {
			t.Fatal(err)
		}
		child, _, err := readThreadJournal(proxy.replayStore, workspace, "child")
		if err != nil || child.LifecycleState != want || child.Turns != turns {
			t.Fatalf("child = %s after %d turns, want %s after %d (%v)", child.LifecycleState, child.Turns, want, turns, err)
		}
	}
	f.event("child", map[string]any{"type": "codex_turn_started", "codex_turn_id": "follow-up"})
	refresh("working", 2)
	traceChildResult(f, "child", "tree", "follow-up", "/root/child", "interrupted")
	refresh("blocked", 2) // The failure arrived before any provider request.
	session, err := proxy.replayStore.readRetainedSession(storageSessionName("tree"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if session.Files[journalFilename(workspace, "child")] {
		t.Fatal("parent session retained the child's journal")
	}
}

func TestJournalTraceChildResultIdentityAndFailure(t *testing.T) {
	for _, test := range []struct {
		name, parent, turn, agent, want string
		status                          any
	}{
		{"wrong parent", "other", "current", "/root/child", "working", map[string]any{"completed": "done"}},
		{"old turn", "tree", "old", "/root/child", "working", map[string]any{"completed": "done"}},
		{"wrong path", "tree", "current", "/root/sibling", "working", map[string]any{"completed": "done"}},
		{"unknown status", "tree", "current", "/root/child", "working", "done"},
		{"failed", "tree", "current", "/root/child", "blocked", map[string]any{"errored": "host error"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy, workspace := mountFixture(t)
			treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Integrate"), State: new("working"), Agent: "/root/child"})
			if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, "child", "current"); err != nil {
				t.Fatal(err)
			}
			if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "working", ""); err != nil {
				t.Fatal(err)
			}
			f := newNativeTraceFixture(t)
			proxy.nativeTrace = &nativeToolTrace{directory: f.root}
			f.event("child", map[string]any{"type": "codex_turn_started", "codex_turn_id": "current"})
			traceChildResult(f, "child", test.parent, test.turn, test.agent, test.status)
			nodes, err := proxy.commentary.journalReader(t.Context(), workspace+"\x00tree", "tree", "", "", nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if mount, ok := mountFind(nodes, "/1/@child"); !ok || mount.State != test.want {
				t.Fatalf("mount = %+v, want %s", mount, test.want)
			}
			if _, err := proxy.commentary.journalPublisher(t.Context(), workspace+"\x00tree", "tree", "close", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err == nil {
				t.Fatal("unsettled or failed child permitted parent completion")
			}
		})
	}
}

func TestJournalTraceCorruptChildNotRewritten(t *testing.T) {
	proxy, workspace := mountFixture(t)
	treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Integrate"), State: new("working"), Agent: "/root/child"})
	if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, "child", "current"); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "working", ""); err != nil {
		t.Fatal(err)
	}
	j, _, err := readThreadJournal(proxy.replayStore, workspace, "child")
	if err != nil {
		t.Fatal(err)
	}
	j.Receipts = nil
	if err := writeThreadJournal(proxy.replayStore, j); err != nil {
		t.Fatal(err)
	}
	f := newNativeTraceFixture(t)
	proxy.nativeTrace = &nativeToolTrace{directory: f.root}
	f.event("child", map[string]any{"type": "codex_turn_started", "codex_turn_id": "current"})
	traceChildResult(f, "child", "tree", "current", "/root/child", map[string]any{"completed": "done"})
	_, readErr := proxy.commentary.journalReader(t.Context(), workspace+"\x00tree", "tree", "", "", nil, "")
	after, _, afterErr := readThreadJournal(proxy.replayStore, workspace, "child")
	if after.LifecycleState != j.LifecycleState || readErr == nil || afterErr == nil {
		t.Fatalf("corrupt child rewritten from %s to %s, read error=%v, record error=%v", j.LifecycleState, after.LifecycleState, readErr, afterErr)
	}
}

func TestJournalTraceRefreshWaitsForNewerTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proxy, workspace := mountFixture(t)
		if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, "child", "old"); err != nil {
			t.Fatal(err)
		}
		if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "child", "working", ""); err != nil {
			t.Fatal(err)
		}
		f := newNativeTraceFixture(t)
		proxy.nativeTrace = &nativeToolTrace{directory: f.root}
		f.event("child", map[string]any{"type": "codex_turn_started", "codex_turn_id": "old"})
		traceChildResult(f, "child", "tree", "old", "/root/child", "interrupted")
		release, err := proxy.journals.lockState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			_, err := proxy.commentary.journalReader(t.Context(), workspace+"\x00tree", "tree", "", "", nil, "")
			result <- err
		}()
		synctest.Wait()
		// A newer request commits while refresh waits for the journal lease.
		f.event("child", map[string]any{"type": "codex_turn_started", "codex_turn_id": "new"})
		j, _, readErr := readThreadJournal(proxy.replayStore, workspace, "child")
		j.TurnID, j.LifecycleState, j.LifecycleReason = "new", "working", ""
		writeErr := writeThreadJournal(proxy.replayStore, j)
		release()
		if err := <-result; err != nil || readErr != nil || writeErr != nil {
			t.Fatalf("refresh=%v, read=%v, write=%v", err, readErr, writeErr)
		}
		after, _, err := readThreadJournal(proxy.replayStore, workspace, "child")
		if err != nil || after.TurnID != "new" || after.LifecycleState != "working" {
			t.Fatalf("new live turn overwritten: %+v, %v", after, err)
		}
	})
}
