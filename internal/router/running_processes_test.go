package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func processNotify(t *testing.T, u *appServerUI, thread, id, command, status string) {
	t.Helper()
	method := "item/started"
	item := map[string]any{"id": id, "type": "commandExecution", "command": command, "status": status}
	if status != "inProgress" {
		method, item["exitCode"] = "item/completed", 0
		if status == "failed" {
			item["exitCode"] = 1
		}
	}
	appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "t", "item": item})
}

func TestRunningProcessesLifecycle(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	u.session.registerThread(appServerThreadInfo{ID: "child", AgentNickname: "worker"})
	appServerTestKeys(t, u, "/ps\r")
	u.statusPanelFrame(80, 18)
	processNotify(t, u, "main", "cmd", "pwd && sleep 1", "inProgress")
	processNotify(t, u, "child", "cmd", "make test", "inProgress")
	processNotify(t, u, "child", "cmd", "make test", "inProgress") // Repeated starts keep host identity.
	if u.runningProcessCount() != 2 {
		t.Fatal("Main and child commands were not counted once each")
	}
	panel := func() string { return ansi.Strip(strings.Join(u.statusPanelFrame(80, 18), "\n")) }
	if got := panel(); !strings.Contains(got, "main") || !strings.Contains(got, "worker") || !strings.Contains(got, "pwd && sleep 1") {
		t.Fatalf("missing commands or owners: %s", got)
	}
	// A yielded command can outlive the turn that started it. Only its host
	// completion removes it, including when the UI still has output to roll.
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t", "status": "completed"}})
	if u.runningProcessCount() != 2 || !u.sessionAnimating() {
		t.Fatal("yielded command stopped counting or animating")
	}
	appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{"threadId": "child", "turnId": "t", "itemId": "cmd", "delta": strings.Repeat("output\n", 30)})
	processNotify(t, u, "child", "cmd", "make test", "completed")
	if u.runningProcessCount() != 1 || strings.Contains(panel(), "make test") {
		t.Fatal("host-completed output remained in the process list")
	}
	entries := make([]activityPaneEntry, liveActivityFeedLimit)
	for i := range entries {
		entries[i] = activityPaneEntry{Seq: u.view.lastSeq + uint64(i) + 1, Agent: "Main", Kind: "text", Text: "Later event"}
	}
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: entries})
	u.session.seq = u.view.lastSeq
	if u.runningProcessCount() != 1 || !strings.Contains(panel(), "pwd && sleep 1") {
		t.Fatal("transcript retention lost a running command")
	}
	processNotify(t, u, "main", "cmd", "pwd && sleep 1", "failed")
	if u.runningProcessCount() != 0 || !strings.Contains(panel(), "No running processes") || strings.Contains(u.stateLabel(u.now()), "running processes") {
		t.Fatal("last completion did not clear the count and dialog")
	}
}

func TestRunningProcessesTrackedCompletion(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	script := "pwd && sleep 1"
	processNotify(t, u, "main", "cmd", "/usr/bin/bash -lc "+quoteShellWord(script), "inProgress")
	report := dialExecTrackReport(t, hub, script)
	report.send(
		execsegment.Message{Type: execsegment.Begin, Index: 0},
		execsegment.Message{Type: execsegment.End, Index: 0, Code: new(0)},
		execsegment.Message{Type: execsegment.Begin, Index: 1},
	)
	awaitMain(t, u, "Running sleep 1")
	if u.runningProcessCount() != 1 {
		t.Fatal("segments changed the host process count")
	}
	processNotify(t, u, "main", "cmd", "/usr/bin/bash -lc "+quoteShellWord(script), "completed")
	if u.runningProcessCount() != 0 {
		t.Fatal("completed command remained listed while its segment report drained")
	}
}

func TestRunningProcessesRestoredAndSwitched(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	u.restoreHistory([]appServerHistoryTurn{{ID: "old", Status: "completed", Items: []appServerItem{{ID: "old", Type: "commandExecution", Command: "sleep 100", Status: "inProgress"}}}})
	if u.runningProcessCount() != 0 {
		t.Fatal("restored history created a process")
	}
	processNotify(t, u, "main", "cmd", "sleep 100", "inProgress")
	wire := u.client.Input.(*appServerTestInput)
	wire.Reset()
	appServerTestKeys(t, u, "/clear\r")
	r := appServerOneRequest(t, wire, "thread/start", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"fresh","cwd":"/workspace"}}}`, r.ID))
	processNotify(t, u, "main", "late", "sleep 200", "inProgress")
	if u.thread != "fresh" || u.runningProcessCount() != 0 {
		t.Fatal("switch retained old commands or admitted late old events")
	}
}

func TestRunningProcessesInteraction(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	for i := range 20 {
		processNotify(t, u, "main", fmt.Sprint(i), fmt.Sprintf("sleep %d", i+1), "inProgress")
	}
	u.turn, u.status = "active", "Working"
	wire := u.client.Input.(*appServerTestInput)
	wire.Reset()
	for _, closeKey := range []string{"\x1b", "\r", "q", "\x03"} {
		appServerTestKeys(t, u, "/ps\r")
		var screen bytes.Buffer
		if err := u.paint(&screen, 80, 14); err != nil {
			t.Fatal(err)
		}
		if err := u.shell.send("\x1b[6~"); err != nil {
			t.Fatal(err)
		}
		before := u.statusPanel.top
		if err := u.shell.mouse("\x1b[<65;2;3M"); err != nil {
			t.Fatal(err)
		}
		if before == 0 || u.statusPanel.top != before+1 {
			t.Fatal("process dialog did not scroll with keyboard and wheel")
		}
		if err := u.shell.send(closeKey); err != nil {
			t.Fatal(err)
		}
		if u.statusPanel != nil || u.turn != "active" || u.runningProcessCount() != 20 || wire.Len() != 0 || u.draft != "" {
			t.Fatalf("dialog changed work or sent a model request: %s", wire.String())
		}
	}
}

func TestUISnapshotRunningProcesses(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	for _, view := range []*liveActivityView{u.view, u.agents} {
		view.clock, view.painter.Theme = u.clock, livediff.DarkTheme
		view.feedOnly, view.bare = true, true
	}
	u.session.registerThread(appServerThreadInfo{ID: "child", AgentNickname: "worker"})
	for _, thread := range []string{"main", "child"} {
		processNotify(t, u, thread, "cmd", "go test ./...", "inProgress")
		for i := range 3 {
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": fmt.Sprint("after", i), "type": "agentMessage", "text": strings.Repeat("Later transcript\n", 12) + "Last row"}})
		}
	}
	frame, _ := u.mainFrame(80, 12, 0)
	assertNativeUISnapshot(t, "running-processes-composer", frame)
	assertNativeUISnapshot(t, "running-processes-activity", u.agents.render(70, 8, now))
	uisnapshot.AssertTerminal(t, "testdata/snapshots/running-processes-pulse.txt", append([]string{u.stateLabel(now), u.stateLabel(now.Add(time.Second))}, "plain"), 80)
	appServerTestKeys(t, u, "/ps\r")
	assertNativeUISnapshot(t, "running-processes-dialog", u.statusPanelFrame(80, 16))
	u.view.painter.Theme = livediff.LightTheme
	uisnapshot.AssertTerminal(t, "testdata/snapshots/running-processes-light.txt", append(u.statusPanelFrame(36, 14), "plain"), 36)
	for _, thread := range []string{"main", "child"} {
		processNotify(t, u, thread, "cmd", "go test ./...", "completed")
	}
	assertNativeUISnapshot(t, "running-processes-empty", u.statusPanelFrame(60, 12))
}
