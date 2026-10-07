package router

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

type runtimeTaskTestClient struct {
	*runtimeTestClient
	stops []string
	err   error
}

func (f *runtimeTaskTestClient) StopTask(_ context.Context, id string) error {
	f.stops = append(f.stops, id)
	return f.err
}

func runtimeEvidenceEvent(t *testing.T, u *appServerUI, e session.Event) {
	t.Helper()
	if err := u.runtimeEvent(e); err != nil {
		t.Fatal(err)
	}
}

func runtimeEvidenceTask(t *testing.T, u *appServerUI, role string, task session.Task) {
	t.Helper()
	runtimeEvidenceEvent(t, u, session.Event{Kind: "task", Role: role, Task: &task})
}

func TestNativeRuntimeEvidenceUsageReplacement(t *testing.T) {
	for _, command := range []string{"/usage", "/session"} {
		t.Run(command, func(t *testing.T) {
			u, client := runtimeTestUI(t)
			runtimeKeys(t, u, command+"\r")
			if u.statusPanel == nil || u.statusPanel != u.runtime.usagePanel || u.draft != "" || len(client.sent) != 0 {
				t.Fatal("usage intent did not open the shared status dialog locally")
			}
			panel := u.statusPanel
			first := &session.Usage{CostUSD: new(0.12), Models: map[string]session.ModelUsage{"claude-sonnet": {Input: new(uint64(100)), Output: new(uint64(20)), CacheRead: new(uint64(30))}}}
			runtimeEvidenceEvent(t, u, session.Event{Kind: "usage", Usage: first})
			latest := &session.Usage{Models: map[string]session.ModelUsage{"claude-opus": {Input: new(uint64(140)), Output: new(uint64(0))}}}
			runtimeEvidenceEvent(t, u, session.Event{Kind: "usage", Usage: latest})
			runtimeEvidenceEvent(t, u, session.Event{Kind: "usage"})
			if u.runtime.usage != latest || u.statusPanel != panel {
				t.Fatal("cumulative usage replaced the dialog or lost the latest snapshot")
			}
			want := []statusField{
				{group: "Usage", label: "Scope", value: "Latest cumulative native query totals, including subagents and retained turns"},
				{group: "Model · claude-opus", label: "Input tokens", value: "140"},
				{group: "Model · claude-opus", label: "Output tokens", value: "0"},
			}
			if !slices.Equal(panel.fields, want) {
				t.Fatalf("latest optional fields: %+v, want %+v", panel.fields, want)
			}
			if len(u.view.entries) != 0 || len(client.sent) != 0 {
				t.Fatal("usage display created transcript entries or native requests")
			}
			if err := u.shell.send("\x1b"); err != nil {
				t.Fatal(err)
			}
			if u.statusPanel != nil {
				t.Fatal("shared status dialog did not close")
			}
			runtimeEvidenceEvent(t, u, session.Event{Kind: "usage", Usage: first})
			if !slices.Equal(panel.fields, want) {
				t.Fatal("late report mutated a closed dialog")
			}
		})
	}
}

func TestNativeRuntimeEvidenceLimitsAreOptionalAndReplace(t *testing.T) {
	u, _ := runtimeTestUI(t)
	runtimeKeys(t, u, "/usage\r")
	if len(u.statusPanel.fields) != 1 || len(u.runtime.limits) != 0 {
		t.Fatal("usage absence fabricated optional native data")
	}
	runtimeEvidenceEvent(t, u, session.Event{Kind: "limit", Limit: &session.RateLimit{Window: "five_hour", Scope: "account", Status: "allowed", Utilization: new(0.5)}})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "limit", Limit: &session.RateLimit{Window: "seven_day", Status: "allowed_warning"}})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "limit", Limit: &session.RateLimit{Window: "five_hour", Scope: "account", Status: "rejected"}})
	runtimeEvidenceEvent(t, u, session.Event{Kind: "limit"})
	want := []statusField{
		{group: "Usage", value: "No native usage report received"},
		{group: "Limit · five hour · account", label: "Status", value: "rejected"},
		{group: "Limit · seven day", label: "Status", value: "allowed_warning"},
	}
	if len(u.runtime.limits) != 2 || !slices.Equal(u.statusPanel.fields, want) {
		t.Fatalf("optional native limit replacement: %+v", u.statusPanel.fields)
	}
}

func TestNativeRuntimeEvidenceTaskPatchAndLifecycle(t *testing.T) {
	u, _ := runtimeTestUI(t)
	initial := session.Task{ID: "background", ToolID: "tool-1", Kind: "local_bash", Description: "Run focused checks", Status: "running"}
	runtimeEvidenceTask(t, u, "task_started", initial)
	runtimeEvidenceTask(t, u, "task_progress", session.Task{ID: initial.ID, Summary: "Checking package 2"})
	want := initial
	want.Summary = "Checking package 2"
	if got := u.runtime.tasks[initial.ID]; got != want || len(u.runtime.taskOrder) != 1 {
		t.Fatalf("task patch lost identity or duplicated task: %+v", got)
	}
	u.runtime.busy = true
	runtimeEvidenceEvent(t, u, session.Event{Kind: "done"})
	if u.runtime.busy || u.runtime.tasks[initial.ID].Status != "running" {
		t.Fatal("root completion settled a background task")
	}
	runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: initial.ID, Status: "paused"})
	runtimeEvidenceTask(t, u, "task_progress", session.Task{ID: initial.ID, Summary: "Waiting for native continuation"})
	if u.runtime.tasks[initial.ID].Status != "paused" {
		t.Fatal("progress without a lifecycle transition resumed a paused task")
	}
	for _, terminal := range []string{"completed", "failed", "stopped", "killed"} {
		t.Run(terminal, func(t *testing.T) {
			runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: initial.ID, Status: terminal})
			runtimeEvidenceTask(t, u, "task_progress", session.Task{ID: initial.ID, Status: "running", Summary: "Late progress"})
			if u.runtime.tasks[initial.ID].Status != terminal {
				t.Fatal("late progress revived a terminal task")
			}
			runtimeEvidenceTask(t, u, "task_started", session.Task{ID: initial.ID, Status: "running"})
			if u.runtime.tasks[initial.ID].Status != "running" || u.runtime.tasks[initial.ID].Summary != "" {
				t.Fatal("explicit task_started did not restart the task")
			}
		})
	}
	runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: initial.ID, Status: "completed"})
	runtimeEvidenceTask(t, u, "task_updated", session.Task{ID: initial.ID, Status: "running"})
	if u.runtime.tasks[initial.ID].Status != "running" {
		t.Fatal("native lifecycle patch was replaced by inferred terminal permanence")
	}
	u.runtimeEntry(session.Event{Kind: "tool", ID: "nested-tool", Role: "Bash", Caller: initial.ToolID, Text: "go test"})
	for _, view := range []*liveActivityView{u.view, u.agents} {
		if view.entries[len(view.entries)-1].Agent != "Main" {
			t.Fatal("native caller was not associated with its task identity")
		}
	}
	if len(u.session.paths) != 0 || len(u.session.metadata) != 0 || u.client != nil || u.proxy != nil {
		t.Fatal("presentation task identity became an agent principal or Codex transport")
	}
}

func TestNativeRuntimeEvidenceAmbientTaskPatches(t *testing.T) {
	u, _ := runtimeTestUI(t)
	runtimeEvidenceEvent(t, u, session.Event{Kind: "task"})
	runtimeEvidenceTask(t, u, "task_started", session.Task{Status: "running"})
	if len(u.runtime.tasks) != 0 || len(u.runtime.taskOrder) != 0 {
		t.Fatal("missing task identity created a presentation row")
	}
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "ambient", Ambient: true, Kind: "ambient", Description: "Background housekeeping", Status: "running"})
	runtimeEvidenceTask(t, u, "task_progress", session.Task{ID: "ambient", Summary: "Still running"})
	runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "ambient", Status: "completed"})
	if !u.runtime.tasks["ambient"].Ambient || len(u.agents.agents) != 1 || u.agents.agents[0].Name != "/root" || len(u.agents.entries) != 0 || len(u.view.entries) != 0 {
		t.Fatal("ambient patch leaked into native task presentation")
	}
}

func TestNativeRuntimeEvidenceStopTaskIntent(t *testing.T) {
	u, base := runtimeTestUI(t)
	f := &runtimeTaskTestClient{runtimeTestClient: base}
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "other", Description: "Other task", Status: "running"})
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "selected", Description: "Selected task", Status: "running"})
	u.agents.selected, u.shell.focus = "/root/selected", 3
	if u.runtimeCanStopTask() {
		t.Fatal("client without optional task controls exposed stop")
	}
	u.runtime.client = f
	u.agents.selected = "/root/unknown"
	if u.runtimeCanStopTask() {
		t.Fatal("unobserved task identity exposed native control")
	}
	u.agents.selected = "/root/selected"
	if !u.runtimeCanStopTask() {
		t.Fatal("selected active task did not expose optional stop")
	}
	runtimeKeys(t, u, "xx")
	if !slices.Equal(f.stops, []string{"selected"}) || !u.runtime.stoppingTasks["selected"] || u.runtime.tasks["selected"].Status != "running" {
		t.Fatal("stop intent was duplicated or prematurely settled task")
	}
	if u.runtime.tasks["other"].Status != "running" {
		t.Fatal("selected stop mutated another native task")
	}
	runtimeEvidenceEvent(t, u, session.Event{Kind: "task_control", ID: "unrelated", Failed: true})
	if !u.runtime.stoppingTasks["selected"] {
		t.Fatal("foreign receipt released pending task control")
	}
	runtimeEvidenceEvent(t, u, session.Event{Kind: "task_control", ID: "selected", Failed: true, Text: "native rejection"})
	runtimeKeys(t, u, "x")
	if len(f.stops) != 2 {
		t.Fatal("native failure did not release stop retry")
	}
	runtimeEvidenceEvent(t, u, session.Event{Kind: "task_control", ID: "selected"})
	if u.runtime.tasks["selected"].Status != "running" {
		t.Fatal("control acknowledgement completed task")
	}
	runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "selected", Status: "stopped"})
	runtimeKeys(t, u, "x")
	if u.runtimeCanStopTask() || len(f.stops) != 2 || len(base.sent) != 0 {
		t.Fatal("terminal task remained stoppable or control became composer input")
	}
	// A synchronous send failure is also retryable without any receipt.
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "selected", Status: "running"})
	f.err = errors.New("transport rejected")
	runtimeKeys(t, u, "xx")
	if len(f.stops) != 4 || u.runtime.stoppingTasks["selected"] {
		t.Fatal("synchronous failure left stop pending")
	}
}

func TestNativeRuntimeEvidenceTaskDisplayBound(t *testing.T) {
	t.Parallel()
	u, _ := runtimeTestUI(t)
	for i := range 256 {
		runtimeEvidenceTask(t, u, "task_started", session.Task{ID: fmt.Sprint(i), Description: "Task", Status: "running"})
	}
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "overflow", Status: "running"})
	if len(u.runtime.tasks) != 256 || len(u.runtime.taskOrder) != 256 || len(u.agents.agents) != 257 {
		t.Fatal("active display bound was exceeded")
	}
	if _, ok := u.runtime.tasks["overflow"]; ok {
		t.Fatal("active task evicted for overflow")
	}
	runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "127", Status: "completed"})
	runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "overflow", Status: "running"})
	if _, ok := u.runtime.tasks["127"]; ok {
		t.Fatal("settled task was not evicted before active tasks")
	}
	if len(u.runtime.tasks) != 256 || len(u.runtime.taskOrder) != 256 || len(u.agents.agents) != 257 || u.runtime.tasks["0"].Status != "running" || u.runtime.tasks["overflow"].Status != "running" {
		t.Fatal("bounded replacement lost an active task")
	}
}

func TestUISnapshotNativeRuntimeEvidence(t *testing.T) {
	for _, width := range []int{42, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, base := runtimeTestUI(t)
			u.runtime.client = &runtimeTaskTestClient{runtimeTestClient: base}
			runtimeKeys(t, u, "/usage\r")
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-evidence-empty-%d.txt", width)), runtimeFrame(t, u, width, 32))
			runtimeEvidenceEvent(t, u, session.Event{Kind: "usage", Usage: &session.Usage{CostUSD: new(0.1234), Models: map[string]session.ModelUsage{"claude-sonnet": {Input: new(uint64(1200)), Output: new(uint64(180)), Thinking: new(uint64(40)), CacheRead: new(uint64(900)), ContextWindow: new(uint64(200000))}}}})
			runtimeEvidenceEvent(t, u, session.Event{Kind: "limit", Limit: &session.RateLimit{Window: "five_hour", Scope: "account", Status: "allowed", Utilization: new(0.25)}})
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-evidence-usage-%d.txt", width)), runtimeFrame(t, u, width, 32))
			if err := u.shell.send("\x1b"); err != nil {
				t.Fatal(err)
			}
			runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "checks", Kind: "local_bash", Description: "Run focused package checks", Status: "running", Summary: "Checking router"})
			runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "review", Kind: "local_agent", Description: "Review task evidence", Status: "completed", Summary: "Review complete"})
			runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "build", Kind: "local_bash", Description: "Build preview", Status: "failed", Summary: "Compiler rejected input"})
			runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "cancelled", Kind: "local_bash", Description: "Stop background job", Status: "stopped"})
			runtimeEvidenceTask(t, u, "task_notification", session.Task{ID: "waiting", Kind: "local_agent", Description: "Await native continuation", Status: "paused"})
			runtimeEvidenceTask(t, u, "task_progress", session.Task{ID: "pending", Kind: "local_agent", Description: "Await native lifecycle evidence"})
			runtimeEvidenceTask(t, u, "task_started", session.Task{ID: "ambient", Ambient: true, Status: "running"})
			u.shell.focus, u.agents.selected, u.agents.only = 3, "/root/review", true
			u.shell.rosterHeight = 10
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-evidence-tasks-%d.txt", width)), runtimeFrame(t, u, width, 32))
		})
	}
}
