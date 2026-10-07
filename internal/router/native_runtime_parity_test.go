package router

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/session"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func runtimeParityCall(t *testing.T, u *appServerUI, id, role, input, result string, failed bool) {
	t.Helper()
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool", ID: id, Role: role, Text: input})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool_result", ID: id, Text: result, Failed: failed})
}

func TestNativeRuntimeParityToolClassificationAndReplacement(t *testing.T) {
	for _, tc := range []struct{ name, role, input, verb, result string }{
		{"status", "Bash", `{"command":"git status --short","description":"Inspect working tree"}`, "Status", " M example.go"},
		{"shell read", "Bash", `{"command":"cat example.go"}`, "Read", "package example"},
		{"shell search", "Bash", `{"command":"rg -n needle internal"}`, "Search", "internal/example.go:3:needle"},
		{"native read", "Read", `{"file_path":"/work/example.go"}`, "Read", "package example"},
		{"native edit", "Edit", `{"file_path":"/work/example.go","old_string":"old","new_string":"new"}`, "Edit", "File updated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := runtimeTestUI(t)
			runtimeEvidenceEvent(t, u, session.Event{Kind: "tool", ID: "call", Role: tc.role, Text: tc.input})
			if len(u.view.entries) != 1 || u.view.entries[0].Kind != "tool" {
				t.Fatalf("native invocation was not a shared tool row: %+v", u.view.entries)
			}
			seq := u.view.entries[0].Seq
			if !slices.ContainsFunc(u.view.entries[0].blocks, func(b activityui.Block) bool { return b.Verb == tc.verb }) {
				t.Fatalf("missing classified %s operation: %+v", tc.verb, u.view.entries[0].blocks)
			}
			runtimeEvidenceEvent(t, u, session.Event{Kind: "tool_result", ID: "call", Text: tc.result})
			if len(u.view.entries) != 1 || u.view.entries[0].Seq != seq || u.view.entries[0].Kind != "tool" {
				t.Fatal("result appended a row or lost tool identity")
			}
			if strings.Contains(u.view.entries[0].Text, tc.input) {
				t.Fatal("raw provider JSON leaked into shared activity")
			}
		})
	}
}

func TestNativeRuntimeParityBoundedOutputDialog(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			u, _ := runtimeTestUI(t)
			output := strings.Repeat("0123456789 output line\n", activityui.OutputBytes/20) + "final evidence\n"
			runtimeParityCall(t, u, "call", "Bash", `{"command":"make test","description":"Run checks"}`, output, failed)
			entry := u.view.entries[0]
			index := slices.IndexFunc(entry.blocks, func(b activityui.Block) bool { return b.Output != nil })
			if index < 0 {
				t.Fatal("native result has no shared dialog output")
			}
			block := entry.blocks[index]
			retained := block.Output.View()
			if !retained.Done || retained.Dropped == 0 || !strings.Contains(strings.Join(retained.Lines, "\n"), "final evidence") {
				t.Fatalf("output did not settle and retain bounded final evidence: %+v", retained)
			}
			if len(strings.Join(retained.Lines, "\n")) > activityui.OutputBytes || len(strings.Join(block.Tail, "\n")) >= len(output) {
				t.Fatal("native output bypassed shared retention bounds")
			}
			u.view.renderFeed(100, 40)
			if !u.shell.openOutput(u.view, liveActivitySnippet{run: entry.Seq, block: index}) {
				t.Fatal("native invocation cannot open shared output dialog")
			}
			if !slices.ContainsFunc(u.shell.output.pages, func(b activityui.Block) bool { return b.Output == block.Output }) {
				t.Fatal("dialog lost invocation output")
			}
		})
	}
}

func TestNativeRuntimeParityRosterAndChildActivity(t *testing.T) {
	u, _ := runtimeTestUI(t)
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "shell", ToolID: "shell-tool", Kind: "local_bash", Description: "Run checks", Status: "running"})
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "review", ToolID: "child-tool", Kind: "local_agent", Role: "reviewer", Description: "Review change", Status: "running"})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "message", ID: "child-message", Caller: "child-tool", Text: "Child review evidence"})
	runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "shell", Status: "completed", Summary: "Checks passed"})
	if !slices.ContainsFunc(u.agents.agents, func(a activityPaneAgent) bool { return a.Name == "/root" }) {
		t.Fatal("root is absent from native roster")
	}
	if slices.ContainsFunc(u.agents.agents, func(a activityPaneAgent) bool { return a.Name == "/root/shell" }) {
		t.Fatal("background shell job was presented as an agent")
	}
	if !slices.ContainsFunc(u.agents.entries, func(e liveActivityRecord) bool {
		return e.CallID == "child-message" && e.Text == "Child review evidence"
	}) {
		t.Fatal("actual child message is absent from Activity")
	}
	feed := ansi.Strip(strings.Join(u.view.render(100, 80, u.now()), "\n"))
	if strings.Count(feed, "Run checks") != 1 || strings.Count(feed, "Checks passed") != 1 {
		t.Fatalf("completed shell lifecycle duplicated description or summary:\n%s", feed)
	}
}

func TestNativeRuntimeParityFullNoticeTranscript(t *testing.T) {
	u, _ := runtimeTestUI(t)
	notice := "Native runtime warning: " + strings.Repeat("long detail ", 20) + "final recovery instruction"
	runtimeEvidenceEvent(t, u, session.Event{Kind: "notice", Text: notice})
	feed := ansi.Strip(strings.Join(u.view.render(48, 80, u.now()), "\n"))
	var body []string
	for line := range strings.SplitSeq(feed, "\n") {
		if text, ok := strings.CutPrefix(line, "┃ "); ok {
			body = append(body, text)
		}
	}
	if strings.Join(strings.Fields(strings.Join(body, " ")), " ") != strings.Join(strings.Fields(notice), " ") {
		t.Fatalf("full native notice was not available in transcript:\n%s", feed)
	}
}

func TestNativeRuntimeParitySharedControls(t *testing.T) {
	u, client := runtimeTestUI(t)
	u.runtime.busy = true
	runtimeKeys(t, u, "/lock\r")
	runtimeKeys(t, u, "\x03")
	if !u.interruptLocked || client.interrupts != 0 || len(client.sent) != 0 {
		t.Fatal("shared lock did not guard native interruption")
	}
	runtimeKeys(t, u, "/unlock\r")
	runtimeKeys(t, u, "\x03")
	if u.interruptLocked || client.interrupts != 1 {
		t.Fatal("unlock did not restore native cancellation")
	}
	runtimeKeys(t, u, "/live off\r")
	if !u.shell.liveHidden || len(client.sent) != 0 {
		t.Fatal("shared live-pane command escaped to the model")
	}
	runtimeKeys(t, u, "/live on\r")
	if u.shell.liveHidden {
		t.Fatal("shared live-pane toggle did not restore the dock")
	}
	u.runtime.busy = false
	runtimeKeys(t, u, "/status\r")
	if u.statusPanel == nil || len(client.sent) != 0 {
		t.Fatal("shared status dialog is not connected")
	}
}

func TestNativeRuntimeParityIdleLock(t *testing.T) {
	for _, command := range []string{"/unlock", "/quit"} {
		t.Run(command, func(t *testing.T) {
			u, client := runtimeTestUI(t)
			runtimeKeys(t, u, "/lock\r\x03")
			if u.quitRequested || !u.interruptLocked || client.interrupts != 0 {
				t.Fatal("locked idle Ctrl-C exited or interrupted the native client")
			}
			runtimeKeys(t, u, "draft\x03")
			if u.draft != "" || u.quitRequested {
				t.Fatal("lock prevented draft clearing or cleared the exit guard")
			}
			runtimeKeys(t, u, command+"\r")
			if command == "/unlock" {
				runtimeKeys(t, u, "\x03")
			}
			if !u.quitRequested || client.interrupts != 0 || len(client.sent) != 0 {
				t.Fatal("explicit quit or unlocked idle Ctrl-C did not exit locally")
			}
		})
	}
}

func runtimeParitySettledOutput(t *testing.T) *appServerUI {
	t.Helper()
	u, _ := runtimeTestUI(t)
	runtimeParityCall(t, u, "success", "Bash", `{"command":"printf 'first\\nsecond\\n'"}`, "first\nsecond", false)
	if settleActivity(u.now().Add(time.Hour), u.view, u.agents) {
		t.Fatal("output collapsed without a later standalone event")
	}
	runtimeParityCall(t, u, "failure", "Bash", `{"command":"false"}`, "first diagnostic\nsecond diagnostic", true)
	runtimeEvidenceEvent(t, u, session.Event{Kind: "message", ID: "next", Text: "The checks have finished."})
	if settleActivity(u.now(), u.view, u.agents) || !settleActivity(u.now().Add(activityui.OutputDebounce), u.view, u.agents) {
		t.Fatal("native output did not use the shared settling deadline")
	}
	for _, view := range []*liveActivityView{u.view, u.agents} {
		for _, entry := range view.entries {
			switch entry.CallID {
			case "success":
				if !entry.native.collapsed || !entry.native.settled.IsZero() {
					t.Fatal("successful native output remained expanded")
				}
			case "failure":
				if entry.native.collapsed {
					t.Fatal("native failure details collapsed automatically")
				}
			}
		}
	}
	return u
}

func TestNativeRuntimeParityOutputSettles(t *testing.T) {
	runtimeParitySettledOutput(t)
}

func TestNativeRuntimeParityLastOutputSettlesInActivity(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			u, _ := runtimeTestUI(t)
			runtimeParityCall(t, u, "only-command", "Bash", `{"command":"make test"}`, "first output\nsecond output", failed)
			if settleActivity(u.now().Add(time.Hour), u.view, u.agents) {
				t.Fatal("last command collapsed before a later event")
			}
			runtimeEvidenceEvent(t, u, session.Event{Kind: "message", ID: "user", Role: "You", Text: "Follow-up question"})
			if settleActivity(u.now().Add(time.Minute), u.view, u.agents) {
				t.Fatal("user input settled another agent's output")
			}
			runtimeEvidenceEvent(t, u, session.Event{Kind: "message", ID: "final-reply", Text: "The checks have finished."})
			settleActivity(u.now().Add(time.Minute), u.view, u.agents)
			if len(u.agents.entries) != 1 {
				t.Fatal("lifecycle observation duplicated root speech in Activity")
			}
			for _, view := range []*liveActivityView{u.view, u.agents} {
				if view.entries[0].native.collapsed == failed {
					t.Fatalf("wrong last-command settlement: conversation=%t failed=%t", view.conversation, failed)
				}
			}
		})
	}
}

// Use the terminal's SGR parser and painted hit regions, not openOutput.
func runtimeParityClick(t *testing.T, u *appServerUI, view *liveActivityView, width int, match func(activityui.Block) bool) {
	t.Helper()
	frame := runtimeFrame(t, u, width, 44)
	rect := u.shell.layout.codex
	if view == u.agents {
		rect = u.shell.layout.agents
	}
	for i, snippet := range view.feedSnippets {
		block, ok := view.snippetBlock(snippet)
		if !ok || !match(block) {
			continue
		}
		x, y := rect.x+view.feedLeft, rect.y+view.feedTop+i
		runtimeKeys(t, u, fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x, y, x, y))
		if u.shell.output == nil {
			t.Fatalf("painted native click did not open its dialog:\n%s", frame)
		}
		return
	}
	t.Fatalf("native event has no painted click target:\n%s", frame)
}

func TestNativeRuntimeParityClickCommandOutput(t *testing.T) {
	for _, width := range []int{48, 120} {
		for _, pane := range []string{"Main", "Activity"} {
			t.Run(fmt.Sprintf("%s/%d", pane, width), func(t *testing.T) {
				u, _ := runtimeTestUI(t)
				output := "first evidence\nsecond evidence\nfinal evidence"
				runtimeParityCall(t, u, "command", "Bash", `{"command":"make test"}`, output, false)
				view := u.view
				if pane == "Activity" {
					view = u.agents
					u.shell.selectNativePane(2)
				}
				runtimeParityClick(t, u, view, width, func(b activityui.Block) bool { return b.Output != nil })
				runtimeFrame(t, u, width, 44)
				if u.shell.output.laid.Text != output {
					t.Fatalf("clicked command lost retained output: %q", u.shell.output.laid.Text)
				}
				runtimeKeys(t, u, "\x1b")
				u.shell.sequenceAt = time.Now().Add(-time.Second)
				if err := u.shell.flushEscape(); err != nil {
					t.Fatal(err)
				}
				if u.shell.output != nil || len(u.runtime.client.(*runtimeTestClient).sent) != 0 {
					t.Fatal("closing output changed the native conversation")
				}
			})
		}
	}
}

func TestNativeRuntimeParityClickEvents(t *testing.T) {
	for _, width := range []int{48, 120} {
		for _, pane := range []string{"Main", "Activity"} {
			t.Run(fmt.Sprintf("%s/%d", pane, width), func(t *testing.T) {
				u, client := runtimeTestUI(t)
				runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "review", ToolID: "review-tool", Kind: "local_agent", Description: "Review checks", Status: "running"})
				var lines []string
				for i := range 80 {
					lines = append(lines, fmt.Sprintf("Retained review event %d", i))
				}
				text := strings.Join(lines, "\n")
				runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "review", Status: "completed", Summary: text})
				view := u.view
				if pane == "Activity" {
					view = u.agents
					u.shell.selectNativePane(2)
				}
				body := "Review checks · completed\n" + text
				runtimeParityClick(t, u, view, width, func(b activityui.Block) bool { return b.Body == body })
				runtimeFrame(t, u, width, 44)
				if u.shell.output.laid.Text != body || len(client.sent) != 0 {
					t.Fatal("clicking the native event lost its full body or sent model input")
				}
			})
		}
	}
}

func TestUISnapshotNativeRuntimeClickShellTaskStop(t *testing.T) {
	for _, width := range []int{48, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, base := runtimeTestUI(t)
			client := &runtimeTaskTestClient{runtimeTestClient: base}
			u.runtime.client = client
			runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "shell", Kind: "local_bash", Description: "Watch checks", Status: "running"})
			runtimeParityClick(t, u, u.view, width, func(b activityui.Block) bool { return b.Kind == "progress" })
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-shell-stop-%d.txt", width)), runtimeFrame(t, u, width, 44))
			if !u.runtimeCanStopTask() {
				t.Fatal("shell disclosure lost its native stop control")
			}
			runtimeKeys(t, u, "/x")
			if len(client.stops) != 0 || u.shell.output.draft != "x" {
				t.Fatal("dialog search text invoked native stop")
			}
			runtimeKeys(t, u, "\x1b")
			u.shell.sequenceAt = time.Now().Add(-time.Second)
			if err := u.shell.flushEscape(); err != nil {
				t.Fatal(err)
			}
			runtimeKeys(t, u, "xx")
			if !slices.Equal(client.stops, []string{"shell"}) || len(base.sent) != 0 {
				t.Fatal("clicked shell task did not stop exactly once")
			}
			runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "shell", Status: "stopped"})
			runtimeKeys(t, u, "x")
			if u.runtimeCanStopTask() || len(client.stops) != 1 {
				t.Fatal("terminal shell task still exposed a stop action")
			}
		})
	}
}

func TestNativeRuntimeParityClickBackgroundCommandStop(t *testing.T) {
	u, base := runtimeTestUI(t)
	client := &runtimeTaskTestClient{runtimeTestClient: base}
	u.runtime.client = client
	runtimeParityCall(t, u, "command", "Bash", `{"command":"make watch","run_in_background":true}`, "Native command is running in the background", false)
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "other", ToolID: "other-command", Kind: "local_bash", Description: "Other work", Status: "running"})
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "shell", ToolID: "command", Kind: "local_bash", Description: "Watch checks", Status: "running"})
	runtimeParityClick(t, u, u.view, 120, func(b activityui.Block) bool { return b.Output != nil })
	if !u.runtimeCanStopTask() {
		t.Fatal("background command output lost its observed task identity")
	}
	runtimeKeys(t, u, "xx")
	if !slices.Equal(client.stops, []string{"shell"}) || u.runtime.tasks["other"].Status != "running" || len(base.sent) != 0 {
		t.Fatal("command output stopped the wrong task, repeated stop, or sent model input")
	}
	runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "shell", Status: "stopped"})
	if u.runtimeCanStopTask() {
		t.Fatal("command output borrowed another running task's stop control")
	}
}

func TestUISnapshotNativeRuntimeSettledOutput(t *testing.T) {
	for _, width := range []int{48, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u := runtimeParitySettledOutput(t)
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-settled-output-%d.txt", width)), runtimeFrame(t, u, width, 44))
		})
	}
}

func TestNativeRuntimeParityFailedEditKeepsTarget(t *testing.T) {
	u, _ := runtimeTestUI(t)
	runtimeParityCall(t, u, "edit", "Edit", `{"file_path":"/work/example.go","old_string":"old","new_string":"new"}`, "Denied", true)
	block := u.view.entries[0].blocks[0]
	if block.EditSource == "failed" || !strings.Contains(block.Label, "failed") || block.Output == nil {
		t.Fatal("failure marker replaced edit provenance or lost native result")
	}
}

func TestUISnapshotNativeRuntimeParity(t *testing.T) {
	for _, width := range []int{48, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := runtimeTestUI(t)
			runtimeEvidenceEvent(t, u, session.Event{Kind: "message", ID: "root-message", Text: "Inspecting the change and running focused checks."})
			runtimeParityCall(t, u, "status", "Bash", `{"command":"git status --short","description":"Inspect working tree"}`, " M example.go", false)
			runtimeParityCall(t, u, "read", "Bash", `{"command":"cat example.go"}`, "package example\n\nconst answer = 42", false)
			runtimeParityCall(t, u, "search", "Bash", `{"command":"rg -n answer internal"}`, "internal/example.go:3:const answer = 42", false)
			runtimeParityCall(t, u, "edit", "Edit", `{"file_path":"/work/example.go","old_string":"41","new_string":"42"}`, "File updated", false)
			runtimeParityCall(t, u, "checks", "Bash", `{"command":"make test","description":"Run checks"}`, "ok example", false)
			runtimeParityCall(t, u, "failure", "Bash", `{"command":"false","description":"Failure example"}`, "command failed", true)
			runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "review", ToolID: "review-tool", Kind: "local_agent", Role: "reviewer", Description: "Review implementation", Status: "running"})
			runtimeEvidenceEvent(t, u, session.Event{Kind: "message", ID: "child-message", Caller: "review-tool", Text: "Reviewed command lifecycle and shared output."})
			runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "review", Status: "completed", Summary: "Review complete"})
			runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "background", Kind: "local_bash", Description: "Build preview", Status: "running"})
			runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "background", Status: "completed", Summary: "Preview built"})
			runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "active-shell", Kind: "local_bash", Description: "Watch checks", Status: "running"})
			finishPacing(u.view, u.agents)
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-parity-main-%d.txt", width)), runtimeFrame(t, u, width, 44))
			u.shell.focus, u.shell.rosterHeight = 3, 8
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-parity-agents-%d.txt", width)), runtimeFrame(t, u, width, 36))
		})
	}
}
