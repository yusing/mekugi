package router

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func runtimeOutputSnapshot(t *testing.T, u *appServerUI, id, task, text string, truncated, done bool) {
	t.Helper()
	runtimeEvidenceEvent(t, u, session.Event{Kind: "command_output", ID: id, Text: text, Output: &session.CommandOutput{TaskID: task, Truncated: truncated, Done: done}})
}

func TestNativeRuntimeOutputSnapshotKeepsOpenDialogAndNativeAggregate(t *testing.T) {
	u, _ := runtimeTestUI(t)
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool", ID: "command", Role: "Bash", Text: `{"command":"make watch"}`})
	output := u.view.entries[0].native.output
	runtimeOutputSnapshot(t, u, "command", "shell", "first\n", false, false)
	runtimeParityClick(t, u, u.view, 120, func(b activityui.Block) bool { return b.Output != nil })
	runtimeOutputSnapshot(t, u, "command", "shell", "first\nsecond\n", false, false)
	frame := runtimeFrame(t, u, 120, 40)
	if !strings.Contains(frame, "second") || output.View().Done || u.view.entries[0].native.output != output || u.agents.entries[0].native.output != output {
		t.Fatal("streaming replaced its dialog identity or failed to update both shared views")
	}
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool_result", ID: "command", Text: "authoritative full aggregate\n", Failed: false})
	if !output.View().Done || strings.Join(output.View().Lines, "\n") != "authoritative full aggregate" || u.view.entries[0].native.output != output {
		t.Fatal("native foreground aggregate did not settle the original output")
	}
	runtimeOutputSnapshot(t, u, "command", "shell", "late snapshot", false, false)
	if u.view.entries[0].native.running || strings.Join(output.View().Lines, "\n") != "authoritative full aggregate" {
		t.Fatal("late polling revived a completed command")
	}
}

func TestNativeRuntimeOutputEscapeRequestsRedrawWithoutNewEvents(t *testing.T) {
	u, _ := runtimeTestUI(t)
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool", ID: "command", Role: "Bash", Text: `{"command":"make watch"}`})
	runtimeOutputSnapshot(t, u, "command", "shell", "first\n", false, false)
	runtimeParityClick(t, u, u.view, 120, func(b activityui.Block) bool { return b.Output != nil })
	runtimeKeys(t, u, "\x1b")
	u.shell.sequenceAt = time.Now().Add(-time.Second)
	u.dirty = false
	if err := u.drainKeys(make(chan byte)); err != nil {
		t.Fatal(err)
	}
	if u.shell.output != nil || !u.dirty {
		t.Fatal("delayed Escape did not close and redraw the shared overlay")
	}
}

func TestNativeRuntimeBackgroundOutputWaitsForTaskAndKeepsTruncation(t *testing.T) {
	u, _ := runtimeTestUI(t)
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool", ID: "command", Role: "Bash", Text: `{"command":"make watch","run_in_background":true}`})
	runtimeOutputSnapshot(t, u, "command", "shell", "first\n", false, false)
	output := u.view.entries[0].native.output
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool_result", ID: "command", Text: "Native background launch placeholder", Output: &session.CommandOutput{TaskID: "shell"}})
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "shell", ToolID: "command", Kind: "local_bash", Status: "running"})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "done"})
	if output.View().Done || !u.view.entries[0].native.running || !strings.Contains(strings.Join(u.view.entries[0].outputTail, "\n"), "first") {
		t.Fatal("background launch or root completion settled or replaced live output")
	}
	runtimeOutputSnapshot(t, u, "command", "shell", "retained tail\n", true, false)
	if !output.View().PrefixOmitted || output.View().Dropped != 0 || strings.Join(output.View().Lines, "\n") != "retained tail" {
		t.Fatal("snapshot was appended or invented an omitted line count")
	}
	runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "shell", Status: "failed"})
	if !output.View().Done || u.view.entries[0].native.running || u.view.entries[0].native.status != "failed" {
		t.Fatal("actual background terminal edge failed to settle retained output")
	}
}

func TestUISnapshotNativeRuntimeStreamingOutputDialog(t *testing.T) {
	for _, width := range []int{48, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := runtimeTestUI(t)
			runtimeEvidenceEvent(t, u, session.Event{Kind: "tool", ID: "command", Role: "Bash", Text: `{"command":"make watch"}`})
			runtimeOutputSnapshot(t, u, "command", "shell", "Build started\nWatching source files\n", true, false)
			runtimeParityClick(t, u, u.view, width, func(b activityui.Block) bool { return b.Output != nil })
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-streaming-output-%d.txt", width)), runtimeFrame(t, u, width, 32))
		})
	}
}

func TestNativeRuntimeBackgroundTerminalSnapshotUpdatesBothViews(t *testing.T) {
	u, _ := runtimeTestUI(t)
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool", ID: "command", Role: "Bash", Text: `{"command":"make watch","run_in_background":true}`})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool_result", ID: "command", Output: &session.CommandOutput{TaskID: "shell"}})
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "shell", ToolID: "command", Kind: "local_bash", Status: "running"})
	runtimeOutputSnapshot(t, u, "command", "shell", "first\n", false, false)
	runtimeOutputSnapshot(t, u, "command", "shell", "final\n", false, true)
	runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "shell", Status: "completed"})
	runtimeOutputSnapshot(t, u, "command", "shell", "late\n", false, false)
	for name, view := range map[string]*liveActivityView{"main": u.view, "activity": u.agents} {
		entry := view.entries[0]
		if strings.Join(entry.outputTail, "\n") != "final" || entry.native.running || !entry.native.output.View().Done || strings.Join(entry.native.output.View().Lines, "\n") != "final" {
			t.Fatalf("%s did not keep the final snapshot after settlement and late polling: %+v", name, entry)
		}
	}
}
