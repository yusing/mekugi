package launch

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
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

// processDialog paints the inline process surface in isolation.
func processDialog(u *appServerUI, width, height int) []string {
	rows := make([]string, height)
	u.detailFiles.prepareOutput(&u.attached.session, terminalTest(u).Attachment)
	terminalTest(u).Dialog.Paint(rows, width, height)
	return rows
}

func TestRunningProcessesLifecycle(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	registerThread(&u.attached.session, binding.ThreadInfo{ID: "child", AgentNickname: "worker"})
	appServerTestKeys(t, u, "/ps\r")
	if got := drawOutputDialog(terminalTest(u)); !strings.Contains(got, "No running processes") {
		t.Fatalf("missing empty state: %s", got)
	}
	// Commands that start while the dialog is open join it in start order,
	// shown as transcript rows show them, without Mekugi instrumentation.
	guarded, err := shellinstrumentation.RewriteCommands("git fetch && go test", displayGuardHelper, displayGuardDirectory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	command := "/usr/bin/bash -lc " + quoteShellWord(shellinstrumentation.ShScript("/private/exec-track.sh", guarded))
	processNotify(t, u, "main", "cmd", command, "inProgress")
	processNotify(t, u, "child", "cmd", "make test", "inProgress")
	processNotify(t, u, "child", "cmd", "make test", "inProgress") // Repeated starts keep host identity.
	if runningProcessCount(&u.attached.session) != 2 {
		t.Fatal("Main and child commands were not counted once each")
	}
	if got := drawOutputDialog(terminalTest(u)); !strings.Contains(got, "main · git fetch && go test") || !strings.Contains(got, "worker · make test") ||
		strings.Contains(got, "No running processes") || strings.Contains(got, "exec-track") || strings.Contains(got, displayGuardHelper) {
		t.Fatalf("missing process list or owners, or leaked instrumentation: %s", got)
	}
	// A yielded command can outlive the turn that started it. Only its host
	// completion removes it, including when the UI still has output to roll.
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t", "status": "completed"}})
	if runningProcessCount(&u.attached.session) != 2 || !publishTestPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, u.settings, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side).Status.Animating {
		t.Fatal("yielded command stopped counting or animating")
	}
	if u.attached.session.Transcript("main", "t").Turn != nil || !u.attached.session.Commands()[[3]string{"main", "t", "cmd"}].Entry.Native.Invocation.Snapshot().Running {
		t.Fatal("command lifetime followed its closed turn scope")
	}
	for _, key := range []string{"\x1b[B", "\r"} {
		if err := terminalTest(u).send(key); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 30 {
		appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{"threadId": "child", "turnId": "t", "itemId": "cmd", "delta": fmt.Sprintf("output %02d\n", i)})
		if i == 0 {
			if got := drawOutputDialog(terminalTest(u)); !strings.Contains(got, "● live") || !strings.Contains(got, "output 00") {
				t.Fatalf("dialog did not stream live output: %s", got)
			}
		}
	}
	if got := drawOutputDialog(terminalTest(u)); !strings.Contains(got, "output 29") || strings.Contains(got, "output 00") {
		t.Fatalf("dialog did not follow streamed output: %s", got)
	}
	// The open page keeps its final output; the next /ps omits it.
	processNotify(t, u, "child", "cmd", "make test", "completed")
	if got := drawOutputDialog(terminalTest(u)); runningProcessCount(&u.attached.session) != 1 || strings.Contains(got, "● live") || !strings.Contains(got, "output 29") || !strings.Contains(got, "exit 0") {
		t.Fatalf("completed command lost its open page: %s", got)
	}
	entries := make([]sessionpresentation.ActivityEntry, liveActivityFeedLimit)
	for i := range entries {
		entries[i] = sessionpresentation.ActivityEntry{Seq: u.attached.source.LastSeq + uint64(i) + 1, Agent: "Main", Kind: "text", Text: "Later event"}
	}
	u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Entries: entries})
	u.attached.session.Activity().AdvanceSequence(u.attached.source.LastSeq)
	reopen := func() {
		if err := terminalTest(u).send("\x1b"); err != nil {
			t.Fatal(err)
		}
		appServerTestKeys(t, u, "/ps\r")
	}
	reopen()
	if got := drawOutputDialog(terminalTest(u)); runningProcessCount(&u.attached.session) != 1 || !strings.Contains(got, "git fetch && go test") || strings.Contains(got, "make test") {
		t.Fatalf("reopened list kept a completed command or lost a running one: %s", got)
	}
	processNotify(t, u, "main", "cmd", command, "failed")
	reopen()
	if got := drawOutputDialog(terminalTest(u)); runningProcessCount(&u.attached.session) != 0 || !strings.Contains(got, "No running processes") || strings.Contains(u.stateLabel(presentationTime(u.clock)), "running processes") {
		t.Fatalf("last completion did not clear the count and dialog: %s", got)
	}
}

func TestRunningProcessesTrackedCompletion(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	script := "pwd && sleep 1"
	processNotify(t, u, "main", "cmd", "/usr/bin/bash -lc "+quoteShellWord(script), "inProgress")
	report := dialExecTrackReport(t, hub, script)
	report.send(
		segmenttransport.Message{Type: segmenttransport.Begin, Index: 0},
		segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)},
		segmenttransport.Message{Type: segmenttransport.Begin, Index: 1},
	)
	awaitMain(t, u, "Running sleep 1")
	if runningProcessCount(&u.attached.session) != 1 {
		t.Fatal("segments changed the host process count")
	}
	// Tracked output retains a copied Activity row; late host identity belongs
	// to the canonical running operation, not that earlier display snapshot.
	appServerTestNotify(t, u, "item/commandExecution/terminalInteraction", map[string]any{"threadId": "main", "turnId": "t", "itemId": "cmd", "processId": "42"})
	appServerTestKeys(t, u, "/ps\r")
	stop := terminalTest(u).Dialog.Key("x")
	if stop.Process == nil || stop.Process.ID != "42" || stop.Process.Item != "cmd" {
		t.Fatal("tracked command lost late Stop identity")
	}
	processNotify(t, u, "main", "cmd", "/usr/bin/bash -lc "+quoteShellWord(script), "completed")
	if runningProcessCount(&u.attached.session) != 0 {
		t.Fatal("completed command remained listed while its segment report drained")
	}
}

func TestRunningProcessesRestoredAndSwitched(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	u.restoreHistory([]binding.HistoryTurn{{ID: "old", Status: "completed", Items: []binding.ThreadItem{{ID: "old", Type: "commandExecution", Command: "sleep 100", Status: "inProgress"}}}})
	if runningProcessCount(&u.attached.session) != 0 {
		t.Fatal("restored history created a process")
	}
	processNotify(t, u, "main", "cmd", "sleep 100", "inProgress")
	wire := u.Host.Input.(*appServerTestInput)
	wire.Reset()
	appServerTestKeys(t, u, "/clear\r")
	r := appServerOneRequest(t, wire, "thread/start", "")
	appServerTestKeys(t, u, "/ps\r")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"fresh","cwd":"/workspace"}}}`, r.ID))
	processNotify(t, u, "main", "late", "sleep 200", "inProgress")
	if u.Thread != "fresh" || runningProcessCount(&u.attached.session) != 0 || terminalTest(u).Dialog != nil {
		t.Fatal("switch retained old commands, their dialog, or admitted late old events")
	}
}

func TestRunningProcessesInteraction(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	for i := range 20 {
		processNotify(t, u, "main", fmt.Sprint(i), fmt.Sprintf("sleep %d", i+1), "inProgress")
	}
	u.Turn, u.attached.presentation.Status.Text = "active", "Working"
	wire := u.Host.Input.(*appServerTestInput)
	wire.Reset()
	for _, closeKey := range []string{"\x1b", "q", "\x03"} {
		appServerTestKeys(t, u, "/ps\r")
		drawOutputDialog(terminalTest(u))
		d := terminalTest(u).Dialog
		if err := terminalTest(u).send("\x1b[B"); err != nil {
			t.Fatal(err)
		}
		if got := drawOutputDialog(terminalTest(u)); d.Page != 1 || !strings.Contains(got, "sleep 2") || !strings.Contains(got, "2 / 20") {
			t.Fatalf("Down did not select the next process: %s", got)
		}
		x, y := dialogFramePoint(t, outputDialogScreen(terminalTest(u)), "main · sleep 1 ")
		terminalTest(u).outputMouse(0, x, y, false)
		if d.Page != 0 || !d.ProcessDetail {
			t.Fatal("list click did not open its process")
		}
		if err := terminalTest(u).send(closeKey); err != nil {
			t.Fatal(err)
		}
		if terminalTest(u).Dialog != nil || u.Turn != "active" || runningProcessCount(&u.attached.session) != 20 || wire.Len() != 0 || u.Text != "" {
			t.Fatalf("dialog changed work or sent a model request: %s", wire.String())
		}
	}
}

func TestUISnapshotRunningProcesses(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	for _, view := range []*activityAdapter{u.testActivity(u.shell.Transcript), u.testActivity(u.shell.Agents)} {
		view.Clock, view.Painter.Theme = u.clock, diffrender.DarkTheme
		view.FeedOnly, view.Bare = true, true
	}
	registerThread(&u.attached.session, binding.ThreadInfo{ID: "child", AgentNickname: "worker"})
	for _, thread := range []string{"main", "child"} {
		command := "go test ./..."
		if thread == "child" {
			command = "cd ~/projects/example && go test ./... -run TestBackgroundProcess -count=1"
		}
		processNotify(t, u, thread, "cmd", command, "inProgress")
		appServerTestNotify(t, u, "item/commandExecution/terminalInteraction", map[string]any{"threadId": thread, "turnId": "t", "itemId": "cmd", "processId": "42"})
		for i := range 3 {
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": map[string]any{"id": fmt.Sprint("after", i), "type": "agentMessage", "text": strings.Repeat("Later transcript\n", 12) + "Last row"}})
		}
	}
	frame, _ := u.testMainFrame(80, 12, 0)
	assertNativeUISnapshot(t, "running-processes-composer", frame)
	assertNativeUISnapshot(t, "running-processes-activity", u.testActivity(u.shell.Agents).Render(70, 8, now))
	uisnapshot.AssertTerminal(t, "testdata/snapshots/running-processes-pulse.txt", append([]string{u.stateLabel(now), u.stateLabel(now.Add(time.Second))}, "plain"), 80)
	for _, line := range []string{"ok  \tgithub.com/yusing/mekugi/internal/ui/activity\t0.412s\n", "=== RUN   TestRouter\n"} {
		appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{"threadId": "main", "turnId": "t", "itemId": "cmd", "delta": line})
	}
	now = now.Add(12 * time.Second)
	appServerTestKeys(t, u, "/ps\r")
	uisnapshot.AssertTerminal(t, "testdata/snapshots/running-processes-dialog.txt", append(processDialog(u, 80, 16), "plain"), 80)
	terminalTest(u).outputKey("\r")
	uisnapshot.AssertTerminal(t, "testdata/snapshots/running-processes-detail.txt", append(processDialog(u, 80, 18), "plain"), 80)
	terminalTest(u).outputKey("\x1b[D")
	for _, view := range []*activityAdapter{u.testActivity(u.shell.Transcript), u.testActivity(u.shell.Agents)} {
		view.Painter.Theme = diffrender.LightTheme
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/running-processes-light.txt", append(processDialog(u, 44, 14), "plain"), 44)
	terminalTest(u).outputKey("\x1b[B")
	terminalTest(u).outputKey("\r")
	uisnapshot.AssertTerminal(t, "testdata/snapshots/running-processes-detail-light.txt", append(processDialog(u, 44, 16), "plain"), 44)
	for _, view := range []*activityAdapter{u.testActivity(u.shell.Transcript), u.testActivity(u.shell.Agents)} {
		view.Painter.Theme = diffrender.TerminalTheme
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/running-processes-detail-terminal.txt", append(processDialog(u, 80, 20), "plain"), 80)
	for _, thread := range []string{"main", "child"} {
		processNotify(t, u, thread, "cmd", "go test ./...", "completed")
	}
	if err := terminalTest(u).send("\x1b"); err != nil {
		t.Fatal(err)
	}
	appServerTestKeys(t, u, "/ps\r")
	assertNativeUISnapshot(t, "running-processes-empty", processDialog(u, 60, 12))
}

func TestRunningProcessesDetailNavigation(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	u.testActivity(u.shell.Agents).Clock = u.clock
	for i := range 25 {
		processNotify(t, u, "main", fmt.Sprint(i), fmt.Sprintf("sleep %d", i), "inProgress")
	}
	wire := u.Host.Input.(*appServerTestInput)
	wire.Reset()
	appServerTestKeys(t, u, "/ps\r")
	d := terminalTest(u).Dialog
	for _, last := range []int{4, 24} {
		if last == 24 {
			d.Key("G")
		}
		processDialog(u, 44, 10)
		d.Mouse(0, 3, 8, false) // Padding does not select a hidden process.
		if d.ProcessDetail {
			t.Fatal("padding opened a process")
		}
		d.Mouse(0, 3, 7, false) // Last visible body row.
		d.Mouse(0, 3, 7, true)
		if d.Page != last || !d.ProcessDetail || d.Selection != nil {
			t.Fatalf("last visible row did not open process %d", last)
		}
		d.Key("\x1b[D")
	}
	processDialog(u, 100, 30)
	d.Key("G")
	if got := ansi.Strip(strings.Join(processDialog(u, 44, 10), "\n")); d.Page != 24 || !strings.Contains(got, "sleep 24") {
		t.Fatalf("resize hid selected process: %s", got)
	}
	d.Key("\r")
	got := strings.Join(processDialog(u, 100, 24), "\n")
	if !strings.Contains(got, "Shell details") || !strings.Contains(got, "No output available") || !strings.Contains(got, "Status:  running") {
		t.Fatalf("missing selected process details: %s", got)
	}
	for _, key := range []string{"/", "not-found", "\r"} {
		d.Key(key)
	}
	if got := ansi.Strip(strings.Join(processDialog(u, 100, 24), "\n")); !strings.Contains(got, "no match") {
		t.Fatalf("failed search hid its result: %s", got)
	}
	now = now.Add(124 * time.Second)
	if got := strings.Join(processDialog(u, 100, 24), "\n"); !strings.Contains(got, "Runtime: 2m4s") {
		t.Fatalf("runtime did not advance without output: %s", got)
	}
	if d.Key("x").Process != nil {
		t.Fatal("process without host identity was stoppable")
	}
	appServerTestNotify(t, u, "item/commandExecution/terminalInteraction", map[string]any{"threadId": "main", "turnId": "t", "itemId": "24", "processId": "42"})
	processDialog(u, 100, 24)
	if stop := d.Key("x"); stop.Process == nil || stop.Process.Item != "24" {
		t.Fatal("late host identity did not enable selected detail stop")
	}
	d.Key("\x1b[D")
	if got := strings.Join(processDialog(u, 100, 24), "\n"); d.Page != 24 || d.ProcessDetail || !strings.Contains(got, "Background") {
		t.Fatalf("back lost selection: %s", got)
	}
	processNotify(t, u, "main", "24", "sleep 24", "failed")
	processDialog(u, 100, 24)
	if d.Key("x").Process != nil {
		t.Fatal("completed process remained stoppable")
	}
	d.Key("\r")
	if got := strings.Join(processDialog(u, 100, 24), "\n"); !strings.Contains(got, "Status:  exit 1") {
		t.Fatalf("failed completion lost its status: %s", got)
	}
	for _, key := range []string{"\r", " "} {
		if intent := d.Key(key); intent.Action != terminalsession.DialogClose {
			t.Fatalf("detail close key %q did not dismiss", key)
		}
	}
	if wire.Len() != 0 || runningProcessCount(&u.attached.session) != 24 {
		t.Fatal("navigation dispatched host work or stopped a process")
	}
}

func TestRunningProcessControlIdentity(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	processNotify(t, u, "main", "cmd", "sleep 30", "inProgress")
	processes := u.attached.session.Processes()
	if len(processes) != 1 || processes[0].ID != "" || u.attached.session.AdmitProcess(processes[0]) == nil {
		t.Fatal("missing process identity acquired Stop capability")
	}
	appServerTestNotify(t, u, "item/commandExecution/terminalInteraction", map[string]any{"threadId": "main", "turnId": "t", "itemId": "cmd", "processId": "42"})
	processes = u.attached.session.Processes()
	if len(processes) != 1 || processes[0].ID != "42" || u.attached.session.AdmitProcess(processes[0]) != nil {
		t.Fatal("late process identity did not enable Stop")
	}
	stale := processes[0]
	stale.Turn = "other"
	if u.attached.session.AdmitProcess(stale) == nil {
		t.Fatal("Stop changed its captured invocation")
	}
	processNotify(t, u, "main", "cmd", "sleep 30", "completed")
	if len(u.attached.session.Processes()) != 0 || u.attached.session.AdmitProcess(processes[0]) == nil {
		t.Fatal("completed process retained Stop capability")
	}
}

func TestUISnapshotRunningProcessesInline(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	for _, view := range []*activityAdapter{u.testActivity(u.shell.Transcript), u.testActivity(u.shell.Agents)} {
		view.Clock, view.Painter.Theme = u.clock, diffrender.DarkTheme
	}
	processNotify(t, u, "main", "cmd", "printf '%s\\n' \"$HOME\" && sleep 30", "inProgress")
	appServerTestKeys(t, u, "/ps\r")
	for _, detail := range []bool{false, true} {
		if detail {
			terminalTest(u).outputKey("\r")
		}
		for _, external := range []bool{false, true} {
			u.shell.Shell.SetExternalPanes(external)
			if err := u.testPaint(io.Discard, 100, 32); err != nil {
				t.Fatal(err)
			}
			terminal := terminalTest(u)
			frame, err := terminal.Frame.Render(t.Context(), terminal.frameInput())
			if err != nil || len(frame.Demands) != 0 {
				t.Fatalf("render: %v, demands: %v", err, frame.Demands)
			}
			rows, _ := terminalsession.Screen(100, 32, frame.Rows, -1, -1)
			text := ansi.Strip(strings.Join(rows, "\n"))
			title := "Background"
			if detail {
				title = "Shell details"
			}
			panelRow := -1
			for i, row := range rows {
				if strings.Contains(ansi.Strip(row), title) {
					panelRow = i
				}
			}
			composer := terminal.Main.ComposerRect
			if composer.H == 0 || panelRow < terminal.Shell.Layout.Main.Y+composer.Y+composer.H || strings.Contains(text, "[×]") {
				t.Fatalf("process surface is not below composer: %s", text)
			}
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/running-processes-inline-%t-%t.txt", detail, external), append(rows, "plain"), 100)
		}
	}
}
