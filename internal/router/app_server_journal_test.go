package router

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

func TestNativeJournalCommandsUseStockDisplay(t *testing.T) {
	for _, child := range []bool{false, true} {
		_, proxy, _, workspace := newMekugiTestTransform(t)
		u := newAppServerSessionTestUI(t, workspace)
		u.proxy = proxy
		thread, view := u.thread, u.view
		if child {
			thread, view = "child-thread", u.agents
			u.session.path(thread)
		}
		store, err := openMekugiReplayStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		proxy.replayStore = store
		const command = "mjournal --journal-once http://127.0.0.1/internal/commentary Y2VsbA.token '%7B%22op%22%3A%22add%22%7D'"
		history := mekugiHistory{ExecutingThread: thread, Script: "retained source", CarrierPayload: `const command = "mjournal --journal-once http://127.0.0.1/internal/commentary Y2VsbA.token '" + encodeURIComponent(JSON.stringify(mutation))`}
		if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"cell": history}); err != nil {
			t.Fatal(err)
		}
		item := appServerItem{ID: "command", Type: "commandExecution", Command: command, ExitCode: new(0), AggregatedOutput: new("command output")}
		for _, method := range []string{"item/started", "item/completed"} {
			appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "turn", "item": item})
		}
		check := func() {
			t.Helper()
			if len(view.entries) != 1 || !strings.Contains(view.entries[0].Text, "--journal-once") || view.entries[0].native == nil {
				t.Fatalf("stock command hidden or classified: %+v", view.entries)
			}
		}
		check()
		*view = *newLiveActivityView()
		turn := appServerHistoryTurn{ID: "turn", Status: "completed", Items: []appServerItem{item}}
		if child {
			u.restoreActivityThread(appServerThreadInfo{ID: thread, Cwd: workspace, Turns: []appServerHistoryTurn{turn}})
		} else {
			u.restoreHistory([]appServerHistoryTurn{turn})
		}
		check()
	}
}

func TestNativeJournalPrecedesNextHostCommand(t *testing.T) {
	_, proxy, _, workspace := newMekugiTestTransform(t)
	u := newAppServerSessionTestUI(t, workspace)
	u.proxy = proxy
	if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, u.thread, "/root", ""); err != nil {
		t.Fatal(err)
	}
	u.journal = proxy.journals.attachNative(workspace, u.thread)
	t.Cleanup(func() { proxy.journals.detachNative(u.journal) })
	ids, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, u.thread, "milestone", []journalMutation{{Op: "add", Text: new("Checked the implementation")}})
	if err != nil {
		t.Fatal(err)
	}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": u.thread, "turnId": "turn", "item": map[string]any{"id": "check", "type": "commandExecution", "command": "git diff --check"}})
	screen := vt.NewEmulator(160, 40)
	defer screen.Close()
	assertOrder := func() {
		t.Helper()
		if len(u.view.entries) != 2 || u.view.entries[0].journal == nil || u.view.entries[0].journal.ID != ids[0] || !strings.Contains(u.view.entries[1].Text, "git diff --check") {
			t.Fatalf("milestone must precede check: %+v", u.view.entries)
		}
		var frame bytes.Buffer
		if err := u.paint(&frame, 160, 40); err != nil {
			t.Fatal(err)
		}
		screen.Write(frame.Bytes())
		visible := screen.String()
		milestone, command := strings.Index(visible, "Checked the implementation"), strings.Index(visible, "git diff --check")
		if milestone < 0 || command <= milestone {
			t.Fatalf("rendered milestone out of order:\n%s", visible)
		}
	}
	assertOrder()
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, u.thread)
	if err != nil || items[0].Reported {
		t.Fatalf("unpainted milestone acknowledged: %+v, %v", items, err)
	}
	u.journal.publish(threadJournal{Sequence: items[0].Updated, Items: items}, true)
	u.applyPendingJournal()
	assertOrder()
}

func TestNativeJournalUnlinkedAnswerWaitsForTerminal(t *testing.T) {
	_, proxy, _, workspace := newMekugiTestTransform(t)
	sink := proxy.journals.attachNative(workspace, "thread-1")
	defer proxy.journals.detachNative(sink)
	_, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", "answer", []journalMutation{{Op: "add", Text: new("Unlinked final answer"), Answer: new(true)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.snapshot()) != 0 {
		t.Fatal("answer exposed before terminal delivery")
	}
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	sink.publish(threadJournal{Sequence: items[0].Updated, Items: items}, true)
	if got := sink.snapshot(); len(got) != 0 {
		t.Fatalf("answer-only terminal emitted a separate native journal report: %+v", got)
	}
	if len(items) != 1 || items[0].Text != "Unlinked final answer" || !items[0].TerminalOnly {
		t.Fatalf("unlinked answer was not retained in journal: %+v", items)
	}
}

func TestNativeJournalSilentDeleteRetractsPendingMilestone(t *testing.T) {
	_, proxy, _, workspace := newMekugiTestTransform(t)
	sink := proxy.journals.attachNative(workspace, "thread-1")
	defer proxy.journals.detachNative(sink)
	ids, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", "milestone", []journalMutation{{Op: "add", Text: new("Superseded")}})
	if err != nil {
		t.Fatal(err)
	}
	view := newLiveActivityView()
	view.applyJournal("thread-1", sink.snapshot()[0])
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", "delete", []journalMutation{{Op: "delete", ID: ids[0]}}); err != nil {
		t.Fatal(err)
	}
	pending := sink.snapshot()
	if len(pending) != 1 || !pending[0].retracted {
		t.Fatalf("missing native retraction: %+v", pending)
	}
	view.applyJournal("thread-1", pending[0])
	if len(view.entries) != 0 {
		t.Fatal("deleted native milestone remained visible")
	}
}
