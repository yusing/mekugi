package router

import (
	"fmt"
	"os"
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

func TestUISnapshotNativeRuntimeChildStreamingOutput(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	u, _ := runtimeTestUI(t)
	u.clock = func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
	feed := runtimeDecodedFrames(t, u)
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "child", ToolID: "spawn", Kind: "local_agent", Description: "Native child conversation", Status: "running"})
	feed(`{"kind":"event","event":{"type":"assistant","parent_tool_use_id":"spawn","message":{"content":[{"type":"tool_use","id":"command","name":"Bash","input":{"command":"printf 'once\\n' >> child-effects.txt; printf 'CHILD_BASH_LIVE\\n'; while [ ! -f child-release.gate ]; do sleep 0.05; done"}}]}}}`)
	feed(`{"kind":"permission","id":"approval","toolUseID":"command","agentID":"child","tool":"Bash","input":{"command":"gated child command"}}`)
	runtimeFrame(t, u, 120, 40)
	runtimeKeys(t, u, "1\r")
	feed(`{"kind":"permission_decision","id":"approval","toolUseID":"command","allow":true}`)
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "shell", ToolID: "command", Kind: "local_bash", Description: "Child gated effect", Status: "running"})
	feed(`{"kind":"command_output","id":"command","taskID":"shell","text":"CHILD_BASH_LIVE\n"}`)
	runtimeKeys(t, u, "\x02"+"3")
	u.agents.only, u.agents.selected = true, runtimeTaskLane("child")
	finishPacing(u.view, u.agents)
	uisnapshot.Assert(t, "testdata/snapshots/native-runtime-child-streaming-output.txt", runtimeFrame(t, u, 120, 40))
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

func TestNativeRuntimeFullOutputRetainsChunksAndRestoresWithoutSpool(t *testing.T) {
	binding := ObservationBinding{Runtime: "claude", Session: "session", Workspace: t.TempDir()}
	directory := t.TempDir()
	service, _, closeService := observationIsolationService(t, directory, binding)
	if err := service.owner.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	u, _ := runtimeTestUI(t)
	u.session.cwd, u.thread = binding.Workspace, binding.Session
	u.attachRuntimeObservation(service)
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool", ID: "command", Role: "Bash", Text: `{"command":"bash output.sh","run_in_background":true}`})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool_result", ID: "command", Output: &session.CommandOutput{TaskID: "shell"}})
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "shell", ToolID: "command", Kind: "local_bash", Status: "running"})
	runtimeOutputSnapshot(t, u, "command", "shell", "native tail\n", true, false)
	output := u.view.entries[0].native.output
	runtimeParityClick(t, u, u.view, 120, func(b activityui.Block) bool { return b.Output != nil })
	runtimeEvidenceTask(t, u, "task_updated", session.Task{ID: "shell", Status: "completed"})
	// Three immutable chunks, including a multi-byte rune at a chunk boundary.
	text := "FULL_OUTPUT_FIRST\n" + strings.Repeat("界cedar\n", (2<<20)/9+1) + "FULL_OUTPUT_LAST\n"
	spool := filepath.Join(t.TempDir(), "native spool.output")
	runtimeEvidenceEvent(t, u, session.Event{Kind: "command_output", ID: "command", Output: &session.CommandOutput{TaskID: "shell", OutputFile: spool, Done: true}})
	if !output.View().PrefixOmitted || strings.Join(output.View().Lines, "\n") != "native tail" {
		t.Fatal("unavailable full output changed the readable tail")
	}
	if err := os.WriteFile(spool, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	runtimeEvidenceEvent(t, u, session.Event{Kind: "command_output", ID: "command", Output: &session.CommandOutput{TaskID: "shell", OutputFile: spool, Done: true}})
	ctx, call, err := u.runtimeOutputScope("command")
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := service.owner.store.lookup(ctx, binding.Workspace, observationKey(call)+"/output")
	if err != nil || !found || record.NativeOutput == nil {
		t.Fatalf("full output receipt missing: found=%v err=%v", found, err)
	}
	retained, err := service.owner.store.readOutputChunks(ctx, record.NativeOutput.Reference)
	if err != nil || retained != text {
		t.Fatalf("immutable UTF-8 recovery differs: bytes=%d want=%d err=%v", len(retained), len(text), err)
	}
	frame := runtimeFrame(t, u, 120, 40)
	if !strings.Contains(frame, "FULL_OUTPUT_LAST") || strings.Contains(frame, "● live") || u.view.entries[0].native.output != output || output.View().PrefixOmitted {
		t.Fatal("late aggregate did not reconcile the original shared dialog")
	}
	closeService()
	if err := os.Remove(spool); err != nil {
		t.Fatal(err)
	}
	resumed, _, _ := observationIsolationService(t, directory, binding)
	if err := resumed.owner.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	fresh, _ := runtimeTestUI(t)
	fresh.session.cwd, fresh.thread = binding.Workspace, binding.Session
	fresh.attachRuntimeObservation(resumed)
	resumeCtx, _, err := fresh.runtimeOutputScope("command")
	if err != nil {
		t.Fatal(err)
	}
	read := executeMRead(resumeCtx, toolWorkerManifest{ReplayDirectory: directory}, []string{record.NativeOutput.Reference, "--max-tokens", "128"})
	if !strings.Contains(read.Stdout, "FULL_OUTPUT_FIRST") || !strings.Contains(read.Stderr, "next_call: mread ") {
		t.Fatalf("fresh managed reader could not recover full output: %+v", read)
	}
	runtimeEvidenceEvent(t, fresh, session.Event{Kind: "tool", ID: "history/command", Role: "Bash", Text: `{"command":"bash output.sh"}`, Historical: true})
	runtimeEvidenceEvent(t, fresh, session.Event{Kind: "tool_result", ID: "history/command", Text: "native launch placeholder", Historical: true})
	for _, view := range []*liveActivityView{fresh.view, fresh.agents} {
		o := view.entries[0].native.output.View()
		if !o.Done || o.PrefixOmitted || o.Dropped == 0 || o.Reference != record.NativeOutput.Reference || !strings.Contains(strings.Join(o.Lines, "\n"), "FULL_OUTPUT_LAST") || strings.Contains(strings.Join(o.Lines, "\n"), "�") {
			t.Fatal("fresh history did not restore bounded complete-output evidence")
		}
	}
	// The same tool ID in another selected session cannot import this output.
	fresh.thread = "foreign-session"
	_, _, err = fresh.runtimeOutputScope("command")
	if err == nil {
		t.Fatal("foreign native session obtained an output storage scope")
	}
	otherBinding := binding
	otherBinding.Session = fresh.thread
	other, _, _ := observationIsolationService(t, directory, otherBinding)
	if err := other.owner.bind(t.Context(), otherBinding); err != nil {
		t.Fatal(err)
	}
	foreign, _ := runtimeTestUI(t)
	foreign.session.cwd, foreign.thread = otherBinding.Workspace, otherBinding.Session
	foreign.attachRuntimeObservation(other)
	runtimeEvidenceEvent(t, foreign, session.Event{Kind: "tool", ID: "history/command", Role: "Bash", Text: `{"command":"bash output.sh"}`, Historical: true})
	runtimeEvidenceEvent(t, foreign, session.Event{Kind: "tool_result", ID: "history/command", Text: "other native result", Historical: true})
	if got := strings.Join(foreign.view.entries[0].native.output.View().Lines, "\n"); got != "other native result" {
		t.Fatal("another bound session imported full command output")
	}
}

func TestNativeRuntimeFullOutputReferenceSurvivesLineBounds(t *testing.T) {
	service, binding, _ := observationHTTPFixture(t)
	if err := service.owner.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	u, _ := runtimeTestUI(t)
	u.session.cwd, u.thread = binding.Workspace, binding.Session
	u.attachRuntimeObservation(service)
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool", ID: "long", Role: "Bash", Text: `{"command":"printf long-output"}`})
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "long-task", ToolID: "long", Kind: "local_bash", Status: "running"})
	text := strings.Repeat("x", 32<<10)
	spool := filepath.Join(t.TempDir(), "long.output")
	if err := os.WriteFile(spool, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	runtimeEvidenceEvent(t, u, session.Event{Kind: "command_output", ID: "long", Output: &session.CommandOutput{TaskID: "long-task", OutputFile: spool, Done: true}})
	view := u.view.entries[0].native.output.View()
	if !view.Truncated || view.Reference == "" {
		t.Fatal("line display bounds hid the complete-output capability")
	}
	ctx, _, err := u.runtimeOutputScope("long")
	if err != nil {
		t.Fatal(err)
	}
	retained, err := service.owner.store.readOutputChunks(ctx, view.Reference)
	if err != nil || retained != text {
		t.Fatal("line bounds changed immutable full output")
	}
	u.setNotice("Later activity", false)
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool", ID: "history/long", Role: "Bash", Text: `{"command":"printf long-output"}`, Historical: true})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "tool_result", ID: "history/long", Text: "native launch", Historical: true})
	for _, entry := range u.view.entries {
		if entry.CallID == "history/long" && entry.native.output.View().Reference != view.Reference {
			t.Fatal("history did not retain the line-bound output capability")
		}
	}
}
