package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
	"github.com/yusing/mekugi/internal/vcsguard"
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

// processDialog paints the open /ps dialog over a blank screen.
func processDialog(u *appServerUI, width, height int) []string {
	rows := make([]string, height)
	u.shell.paintOutput(rows, width, height)
	return rows
}

func TestRunningProcessesLifecycle(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	u.session.registerThread(appServerThreadInfo{ID: "child", AgentNickname: "worker"})
	appServerTestKeys(t, u, "/ps\r")
	if got := drawOutputDialog(u.shell); !strings.Contains(got, "No running processes") {
		t.Fatalf("missing empty state: %s", got)
	}
	// Commands that start while the dialog is open join it in start order,
	// shown as transcript rows show them, without Mekugi instrumentation.
	guarded, err := vcsguard.RewriteCommands("git fetch && go test", displayGuardHelper, displayGuardDirectory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	command := "/usr/bin/bash -lc " + quoteShellWord(execsegment.ShScript("/private/exec-track.sh", guarded))
	processNotify(t, u, "main", "cmd", command, "inProgress")
	processNotify(t, u, "child", "cmd", "make test", "inProgress")
	processNotify(t, u, "child", "cmd", "make test", "inProgress") // Repeated starts keep host identity.
	if u.runningProcessCount() != 2 {
		t.Fatal("Main and child commands were not counted once each")
	}
	if got := drawOutputDialog(u.shell); !strings.Contains(got, "1 main git fetch && go test") || !strings.Contains(got, "2 worker make test") ||
		strings.Contains(got, "No running processes") || strings.Contains(got, "exec-track") || strings.Contains(got, displayGuardHelper) {
		t.Fatalf("missing command tabs or owners, or leaked instrumentation: %s", got)
	}
	// A yielded command can outlive the turn that started it. Only its host
	// completion removes it, including when the UI still has output to roll.
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t", "status": "completed"}})
	if u.runningProcessCount() != 2 || !u.sessionAnimating() {
		t.Fatal("yielded command stopped counting or animating")
	}
	if err := u.shell.send("\x1b[C"); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{"threadId": "child", "turnId": "t", "itemId": "cmd", "delta": fmt.Sprintf("output %02d\n", i)})
		if i == 0 {
			if got := drawOutputDialog(u.shell); !strings.Contains(got, "● live") || !strings.Contains(got, "output 00") {
				t.Fatalf("dialog did not stream live output: %s", got)
			}
		}
	}
	if got := drawOutputDialog(u.shell); !strings.Contains(got, "output 29") || strings.Contains(got, "output 00") {
		t.Fatalf("dialog did not follow streamed output: %s", got)
	}
	// The open page keeps its final output; the next /ps omits it.
	processNotify(t, u, "child", "cmd", "make test", "completed")
	if got := drawOutputDialog(u.shell); u.runningProcessCount() != 1 || strings.Contains(got, "● live") || !strings.Contains(got, "output 29") || !strings.Contains(got, "exit 0") {
		t.Fatalf("completed command lost its open page: %s", got)
	}
	entries := make([]activityPaneEntry, liveActivityFeedLimit)
	for i := range entries {
		entries[i] = activityPaneEntry{Seq: u.view.lastSeq + uint64(i) + 1, Agent: "Main", Kind: "text", Text: "Later event"}
	}
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: entries})
	u.session.seq = u.view.lastSeq
	reopen := func() {
		if err := u.shell.send("\x1b"); err != nil {
			t.Fatal(err)
		}
		appServerTestKeys(t, u, "/ps\r")
	}
	reopen()
	if got := drawOutputDialog(u.shell); u.runningProcessCount() != 1 || !strings.Contains(got, "git fetch && go test") || strings.Contains(got, "make test") {
		t.Fatalf("reopened list kept a completed command or lost a running one: %s", got)
	}
	processNotify(t, u, "main", "cmd", command, "failed")
	reopen()
	if got := drawOutputDialog(u.shell); u.runningProcessCount() != 0 || !strings.Contains(got, "No running processes") || strings.Contains(u.stateLabel(u.now()), "running processes") {
		t.Fatalf("last completion did not clear the count and dialog: %s", got)
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
	appServerTestKeys(t, u, "/ps\r")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"fresh","cwd":"/workspace"}}}`, r.ID))
	processNotify(t, u, "main", "late", "sleep 200", "inProgress")
	if u.thread != "fresh" || u.runningProcessCount() != 0 || u.shell.output != nil {
		t.Fatal("switch retained old commands, their dialog, or admitted late old events")
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
	for _, closeKey := range []string{"\x1b", "q", "\x03"} {
		appServerTestKeys(t, u, "/ps\r")
		drawOutputDialog(u.shell)
		d := u.shell.output
		if err := u.shell.send("\x1b[C"); err != nil {
			t.Fatal(err)
		}
		if got := drawOutputDialog(u.shell); d.page != 1 || !strings.Contains(got, "sleep 2") || !strings.Contains(got, "2 / 20") {
			t.Fatalf("Right did not select the next process: %s", got)
		}
		tab := d.tabs[len(d.tabs)-1]
		u.shell.outputMouse(0, d.rect.x+2+tab.from, d.rect.y+2, false)
		if d.page != tab.page {
			t.Fatal("tab click did not select its process")
		}
		if err := u.shell.send(closeKey); err != nil {
			t.Fatal(err)
		}
		if u.shell.output != nil || u.turn != "active" || u.runningProcessCount() != 20 || wire.Len() != 0 || u.draft != "" {
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
	for _, line := range []string{"ok  \tgithub.com/yusing/mekugi/internal/ui/activity\t0.412s\n", "=== RUN   TestRouter\n"} {
		appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{"threadId": "main", "turnId": "t", "itemId": "cmd", "delta": line})
	}
	now = now.Add(12 * time.Second)
	appServerTestKeys(t, u, "/ps\r")
	uisnapshot.AssertTerminal(t, "testdata/snapshots/running-processes-dialog.txt", append(processDialog(u, 80, 16), "plain"), 80)
	for _, view := range []*liveActivityView{u.view, u.agents} {
		view.painter.Theme = livediff.LightTheme
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/running-processes-light.txt", append(processDialog(u, 44, 14), "plain"), 44)
	for _, thread := range []string{"main", "child"} {
		processNotify(t, u, thread, "cmd", "go test ./...", "completed")
	}
	if err := u.shell.send("\x1b"); err != nil {
		t.Fatal(err)
	}
	appServerTestKeys(t, u, "/ps\r")
	assertNativeUISnapshot(t, "running-processes-empty", processDialog(u, 60, 12))
}
