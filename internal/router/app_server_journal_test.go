package router

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
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

// journalTransportArgument is the quoted payload the generated helper
// appends to its transport prefix.
func journalTransportArgument(t *testing.T, request any) string {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return " '" + url.QueryEscape(string(data)) + "'"
}

// journalTransportFixture retains a lowered journal cell for the UI's thread
// and returns its transport prefix and live authorization token.
func journalTransportFixture(t *testing.T) (*appServerUI, *mekugiProxy, string, string) {
	t.Helper()
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	proxy.commentaryEndpoint = "http://127.0.0.1:1234/internal/commentary"
	const callID = "journal-cell"
	const source = `await journal({op:"add", text:"Checked"});`
	lowered, changed, err := transform.lowerCodeModeCommentary(callID, source)
	if err != nil || !changed {
		t.Fatalf("lowering: %v %v", changed, err)
	}
	token := transform.commentarySubscriptions[0].token
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
	return u, proxy, workerCommand("mjournal", []string{commentaryOnceArgument, proxy.commentaryEndpoint, token}), token
}

func TestNativeJournalTransportHiddenWithDurableProvenance(t *testing.T) {
	u, proxy, prefix, token := journalTransportFixture(t)
	command := workerCommand("/bin/bash", []string{"-c", execsegment.ShScript("/private/exec-track.sh", prefix+journalTransportArgument(t, map[string]any{"op": "add", "title": "Checked"}))})
	var err error
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
		if _, _, shown := u.journalTransport(u.thread, appServerItem{Type: "commandExecution", Command: command}); !shown || u.verifiedJournalCommand(u.thread, appServerItem{Type: "commandExecution", Command: command}) != nil {
			t.Fatalf("classified ordinary command %q", command)
		}
	}
	if u.verifiedJournalCommand("other-thread", appServerItem{Type: "commandExecution", Command: command}) != nil {
		t.Fatal("borrowed another thread's provenance")
	}
	batch := prefix + journalTransportArgument(t, []map[string]any{{"op": "add", "title": "Checked"}})
	if _, _, shown := u.journalTransport(u.thread, appServerItem{Type: "commandExecution", Command: batch}); shown {
		t.Fatal("batched mutation transport visible")
	}
}

// Reads and lists persist no event, so their transport is their only trace.
func TestNativeJournalReadTransportShowsTypedOperation(t *testing.T) {
	u, proxy, prefix, token := journalTransportFixture(t)
	const revision = " 12 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		name    string
		command string
		want    string
	}{
		{"read", prefix + journalTransportArgument(t, map[string]any{"op": "read", "p": "/3/5", "depth": 1}), "Read `journal entries at /3/5 (1 child level)`"},
		{"read continuation", prefix + journalTransportArgument(t, map[string]any{"op": "read", "view": "outline", "depth": 0}) + revision, "Read `journal outline (top level)`"},
		{"agent list", prefix + journalTransportArgument(t, map[string]any{"op": "list", "agent": "worker"}), "List `journal entries for agent worker`"},
		{"agent tasks", prefix + journalTransportArgument(t, map[string]any{"op": "read", "agent": "worker", "view": "tasks", "p": "/7", "depth": 2}), "Read `journal tasks for agent worker at /7 (2 child levels)`"},
		{"own entries", prefix + journalTransportArgument(t, map[string]any{"op": "read", "view": "own"}), "Read `own journal entries`"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := workerCommand("/bin/bash", []string{"-c", execsegment.ShScript("/private/exec-track.sh", test.command)})
			u.view = newLiveActivityView()
			for _, method := range []string{"item/started", "item/completed"} {
				appServerTestNotify(t, u, method, map[string]any{"threadId": u.thread, "turnId": "turn", "item": map[string]any{"id": "read", "type": "commandExecution", "command": command, "exitCode": 0, "aggregatedOutput": `{"ok":true,"items":[]}`}})
			}
			if len(u.view.entries) != 1 || u.view.entries[0].Text != test.want || u.view.entries[0].native.operation != test.want {
				t.Fatalf("live transport entries = %+v, want %q", u.view.entries, test.want)
			}
			if got := commandSegmentText(u.view.entries[0].native, test.command, ""); got != test.want {
				t.Fatalf("tracked segment text = %q, want %q", got, test.want)
			}
			u.view = newLiveActivityView()
			u.restoreHistory([]appServerHistoryTurn{{ID: "turn", Items: []appServerItem{{ID: "read", Type: "commandExecution", Command: command, ExitCode: new(0), AggregatedOutput: new(`{"ok":true,"items":[]}`)}}}})
			if len(u.view.entries) != 1 || u.view.entries[0].Text != test.want {
				t.Fatalf("restored transport entries = %+v, want %q", u.view.entries, test.want)
			}
		})
	}
	proxy.commentary.cancel(token)
	unproven := strings.Replace(prefix, token, "unproven.token", 1) + journalTransportArgument(t, map[string]any{"op": "read"})
	if shown, operation, ok := u.journalTransport(u.thread, appServerItem{Type: "commandExecution", Command: unproven}); !ok || operation != "" || len(shown.CommandActions) != 0 {
		t.Fatalf("unproven read classified: %+v %q %v", shown, operation, ok)
	}
}

func TestUISnapshotNativeJournalPagedReads(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprintf("child=%t", child), func(t *testing.T) {
			var u *appServerUI
			var thread, prefix string
			name := "journal-paged-reads-main"
			if child {
				var command string
				u, thread, command = nativeChildJournalHistoryFixture(t)
				prefix, _, _ = strings.Cut(command, " '")
				name = "journal-paged-reads-child"
				u.session.path(thread)
			} else {
				u, _, prefix, _ = journalTransportFixture(t)
				thread = u.thread
			}
			view := u.view
			if child {
				view = u.agents
			}
			view.conversation = !child
			view.bare, view.feedOnly = child, child
			now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.Local)
			u.clock, view.clock = func() time.Time { return now }, func() time.Time { return now }
			view.painter.Theme = livediff.DarkTheme
			const revision = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			var items []appServerItem
			for i, op := range []string{"read", "read", "list", "list", "read"} {
				command := prefix + journalTransportArgument(t, map[string]any{"op": op, "p": "/12"})
				if i%2 == 1 {
					command += " 4 " + revision
				}
				output := fmt.Sprintf(`{"ok":true,"items":[{"title":"page-%d"}]}`, i+1)
				if op == "list" {
					output = `{"ok":true,"items":[]}`
				}
				item := appServerItem{ID: fmt.Sprint(i), Type: "commandExecution", Command: command, ExitCode: new(0), AggregatedOutput: &output, DurationMS: new(int64(30 + 10*i))}
				if i == 4 {
					item.ExitCode, item.AggregatedOutput = new(1), new("journal changed during read; retry the read")
				}
				appServerTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "turn", "item": item})
				if i == 1 {
					plain := ansi.Strip(strings.Join(view.renderFeed(100, 40).lines, "\n"))
					if strings.Count(plain, "Read") != 2 || !view.entries[1].blocks[0].Running {
						t.Fatalf("running page merged or lost its state: %s", plain)
					}
				}
				now = now.Add(time.Duration(*item.DurationMS) * time.Millisecond)
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "turn", "item": item})
				items = append(items, item)
			}
			check := func(restored bool) {
				t.Helper()
				finishPacing(view)
				feed := view.renderFeed(100, 40)
				plain := ansi.Strip(strings.Join(feed.lines, "\n"))
				if strings.Count(plain, "Read") != 2 || strings.Count(plain, "List") != 1 || strings.Contains(plain, `"ok"`) || !strings.Contains(plain, "(2 results)") || !strings.Contains(plain, "(0 results)") || !strings.Contains(plain, "exit 1") || !strings.Contains(plain, "retry the read") {
					t.Fatalf("journal pages did not group or hid failure: %s", plain)
				}
				for row, line := range feed.lines {
					block, ok := view.snippetBlock(feed.snippets[row])
					if !ok || block.Verb != "Read" || len(block.Members) != 2 {
						continue
					}
					if block.Duration != 70*time.Millisecond || !strings.Contains(ansi.Strip(line), "70ms") || !u.shell.openOutput(view, feed.snippets[row]) || len(u.shell.output.pages) != 2 {
						t.Fatal("group lost duration or page navigation")
					}
					for page := range 2 {
						u.shell.output.showPage(page)
						u.shell.output.layout(90)
						if got := u.shell.output.laid.Text; got != *items[page].AggregatedOutput || u.shell.output.pages[page].Duration != time.Duration(*items[page].DurationMS)*time.Millisecond {
							t.Fatalf("dialog page %d lost output or timing: %q", page, got)
						}
					}
					snapshot := name
					if child && restored {
						snapshot += "-restored"
					}
					uisnapshot.Assert(t, "testdata/snapshots/"+snapshot+".txt", strings.Join(feed.lines, "\n")+"\n")
					uisnapshot.AssertTerminal(t, "testdata/snapshots/"+snapshot+"-style.txt", feed.lines, 100)
					return
				}
				t.Fatal("grouped journal read has no output dialog target")
			}
			check(false)
			// Resume verifies durable provenance and uses the same presentation.
			*view = *newLiveActivityView()
			view.clock, view.painter.Theme = u.clock, livediff.DarkTheme
			view.conversation = !child
			view.bare, view.feedOnly = child, child
			if child {
				u.restoreActivityThread(appServerThreadInfo{ID: thread, Cwd: u.session.cwd, Turns: []appServerHistoryTurn{{ID: "turn", Status: "completed", Items: items}}})
			} else {
				u.restoreHistory([]appServerHistoryTurn{{ID: "turn", Status: "completed", Items: items}})
			}
			check(true)
		})
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
	command := workerCommand("mjournal", []string{commentaryOnceArgument, proxy.commentaryEndpoint, transform.commentarySubscriptions[0].token}) + journalTransportArgument(t, map[string]any{"op": "add", "title": "Checked"})
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
			{name: "tracked proven", thread: child, command: execsegment.ShScript("/private/exec-track.sh", command), hidden: true},
			{name: "other thread", thread: "unrelated", command: command},
			{name: "extra command", thread: child, command: command + "; echo visible"},
			{name: "tracked extra command", thread: child, command: execsegment.ShScript("/private/exec-track.sh", command+"; echo visible")},
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
	command = execsegment.ShScript("/private/exec-track.sh", command)
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
