package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
)

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

func TestNativeJournalTransportHiddenWithDurableProvenance(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	proxy.commentaryEndpoint = "http://127.0.0.1:1234/internal/commentary"
	const callID = "journal-cell"
	const source = `await journal({op:"add", text:"Checked"});`
	lowered, changed, err := transform.lowerCodeModeCommentary(callID, source)
	if err != nil || !changed {
		t.Fatalf("lowering: %v %v", changed, err)
	}
	token := transform.commentarySubscriptions[0].token
	command := workerCommand("mjournal", []string{commentaryOnceArgument, proxy.commentaryEndpoint, token}) + " '%7B%22op%22%3A%22list%22%7D'"
	u := newAppServerSessionTestUI(t, workspace)
	u.proxy = proxy
	proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{ToolName: "exec", Script: source, CarrierPayload: lowered, ExecutingThread: u.thread}
	if err := proxy.replayStore.put(t.Context(), workspace, map[string]mekugiHistory{callID: history}); err != nil {
		t.Fatal(err)
	}

	command = workerCommand("/bin/bash", []string{"-c", command})
	for _, method := range []string{"item/started", "item/completed"} {
		appServerTestNotify(t, u, method, map[string]any{"threadId": u.thread, "turnId": "turn", "item": map[string]any{"id": "internal", "type": "commandExecution", "command": command}})
	}
	if len(u.view.entries) != 0 {
		t.Fatalf("internal transport visible: %+v", u.view.entries)
	}
	// Retiring live authorization and reopening storage must not lose provenance.
	proxy.commentary.cancel(token)
	proxy.replayStore, err = openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	u.restoreHistory([]appServerHistoryTurn{{ID: "turn", Items: []appServerItem{{ID: "internal", Type: "commandExecution", Command: command}}}})
	if len(u.view.entries) != 0 {
		t.Fatal("restored transport visible")
	}
	for _, command := range []string{command + "; echo visible", "echo " + command, strings.Replace(command, token, "unproven.token", 1), "mjournal --help"} {
		if u.internalJournalCommand(u.thread, appServerItem{Type: "commandExecution", Command: command}) {
			t.Fatalf("hid ordinary command %q", command)
		}
	}
	if u.internalJournalCommand("other-thread", appServerItem{Type: "commandExecution", Command: command}) {
		t.Fatal("borrowed another thread's provenance")
	}
}

func nativeChildJournalHistoryFixture(t *testing.T) (*appServerUI, string, string) {
	t.Helper()
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	proxy.commentaryEndpoint = "http://127.0.0.1:1234/internal/commentary"
	const callID = "child-journal-cell"
	const child = "child-thread"
	const source = `await journal({op:"add", text:"Checked"});`
	transform.shellThreadID = child
	lowered, changed, err := transform.lowerCodeModeCommentary(callID, source)
	if err != nil || !changed {
		t.Fatalf("lowering: changed=%v err=%v", changed, err)
	}
	u := newAppServerSessionTestUI(t, workspace)
	u.proxy = proxy
	u.thread = "parent"
	proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	command := workerCommand("mjournal", []string{commentaryOnceArgument, proxy.commentaryEndpoint, transform.commentarySubscriptions[0].token}) + " '%7B%22op%22%3A%22list%22%7D'"
	history := mekugiHistory{ToolName: "exec", Script: source, CarrierPayload: lowered, ExecutingThread: child}
	if err := proxy.replayStore.put(t.Context(), workspace, map[string]mekugiHistory{callID: history}); err != nil {
		t.Fatal(err)
	}
	proxy.commentary.cancel(transform.commentarySubscriptions[0].token)
	proxy.replayStore, err = openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	return u, child, command
}

func TestNativeJournalChildHistoryHidesOnlyProvenTransport(t *testing.T) {
	u, child, command := nativeChildJournalHistoryFixture(t)
	for _, older := range []bool{false, true} {
		for _, test := range []struct {
			name, thread, command string
			hidden                bool
		}{
			{name: "proven", thread: child, command: command, hidden: true},
			{name: "wrapped proven", thread: child, command: workerCommand("/bin/bash", []string{"-c", command}), hidden: true},
			{name: "other thread", thread: "unrelated", command: command},
			{name: "extra command", thread: child, command: command + "; echo visible"},
			{name: "ordinary mention", thread: child, command: "echo mjournal"},
		} {
			t.Run(fmt.Sprintf("older=%t/%s", older, test.name), func(t *testing.T) {
				u.agents = newLiveActivityView()
				u.session.path(test.thread)
				item := appServerItem{ID: "transport", Type: "commandExecution", Command: test.command, ExitCode: new(0), AggregatedOutput: new("transport output")}
				u.restoreActivityThread(appServerThreadInfo{ID: test.thread, Cwd: u.session.cwd, Turns: []appServerHistoryTurn{{ID: "turn", Status: "completed", olderPage: older, Items: []appServerItem{item}}}})
				if got := len(u.agents.entries); test.hidden && got != 0 || !test.hidden && got == 0 {
					t.Fatalf("child history visibility: entries=%d hidden=%v", got, test.hidden)
				}
			})
		}
	}
}

func TestUISnapshotNativeJournalChildHistoryTransport(t *testing.T) {
	u, child, command := nativeChildJournalHistoryFixture(t)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	u.agents.clock = u.clock
	u.agents.painter.Theme = livediff.DarkTheme
	u.agents.bare, u.agents.feedOnly = true, true
	u.session.path(child)
	u.restoreActivityThread(appServerThreadInfo{ID: child, Cwd: u.session.cwd, Turns: []appServerHistoryTurn{{ID: "turn", Status: "completed", Items: []appServerItem{
		{ID: "transport", Type: "commandExecution", Command: command, ExitCode: new(0), AggregatedOutput: new("transport output")},
		{ID: "ordinary", Type: "commandExecution", Command: "echo mjournal", ExitCode: new(0), AggregatedOutput: new("mjournal\n")},
	}}}})
	assertNativeJournalSnapshot(t, "journal-child-history-transport", u.agents.render(100, 12, now))
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
