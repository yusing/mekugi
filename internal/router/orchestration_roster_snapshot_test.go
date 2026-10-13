package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/orchestrate"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotOrchestrationRosterOrder(t *testing.T) {
	v := newLiveActivityView()
	v.painter.Theme = livediff.DarkTheme
	v.orchestration = []activityPaneAgent{{Name: "/Orchestration/main"}, {Name: "/Orchestration/done", Final: true}, {Name: "/Orchestration/review_protocol", Responding: true}}
	v.orchestrationSources = map[string]*liveActivityView{"/Orchestration/main": v}
	var frames []string
	for _, native := range [][]activityPaneAgent{nil, {{Name: "/root/worker"}, {Name: "/root/another"}}} {
		v.agents = native
		frames = append(frames, v.nativeRoster(90, 4, time.Unix(0, 0), true)...)
		if len(v.hits) < 2 || v.hits[0].agent != "/Orchestration/main" || v.hits[1].agent != "/Orchestration/review_protocol" {
			t.Fatal("working thread moved across overflow threshold", v.hits)
		}
	}
	// Actual completion returns the row to stable observation order.
	v.orchestration[2].Responding, v.orchestration[2].Final = false, true
	v.agents = nil
	frames = append(frames, v.nativeRoster(90, 4, time.Unix(0, 0), true)...)
	if v.hits[1].agent != "/Orchestration/done" {
		t.Fatal("completed thread remained working-pinned")
	}
	uisnapshot.Assert(t, "testdata/snapshots/orchestration-roster-order.txt", strings.Join(frames, "\n")+"\n")
}

func TestUISnapshotOrchestrationRosterLifecycle(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	v, child := u.navigation.views["child"], u.orchestrateThreads["child"]
	now := time.Unix(0, 0)
	u.clock = func() time.Time { return now }
	u.agents.clock, u.view.clock = u.clock, u.clock
	u.agents.painter.Theme = livediff.DarkTheme
	u.status, u.awaitingTurn, u.modelsLoading = "Ready", false, true
	u.shell.selectNativePane(4) // No Activity or foreground turn to drive ticks.
	child.batch.Workflow = &orchestrate.Workflow{Assignment: orchestrate.WorkflowAssignment{Kind: orchestrate.WorkflowImplementation}}
	u.picker.orchestration = &orchestrateRunPicker{batches: []orchestrate.Batch{child.batch}}
	var rows []string
	for _, phase := range []orchestrate.WorkflowPhase{orchestrate.PhaseReview, orchestrate.PhaseQueuedReview, orchestrate.PhaseNeedsDecision, orchestrate.PhaseReady} {
		child.batch.Workflow.Phase = phase
		if phase != orchestrate.PhaseReview {
			child.turn = ""
		}
		u.orchestrationRoster()
		u.refreshOrchestratePicker()
		label := orchestrateStateLabel(string(phase))
		if !strings.HasPrefix(u.picker.choices[1].description, label) {
			t.Fatal("picker disagrees with roster phase", u.picker.choices[1])
		}
		if u.statusAnimating() != (phase == orchestrate.PhaseReview) {
			t.Fatal("roster tick does not follow observed lifecycle", phase)
		}
		rows = append(rows, "picker: "+u.picker.choices[1].description)
		for _, offset := range []time.Duration{0, time.Second} {
			rows = append(rows, u.agents.nativeRoster(100, 4, now.Add(offset), true)...)
			rows = append(rows, "plain after roster")
		}
	}
	// Restored labels retain their recorded state without resuming animation.
	u.navigation.retained = []orchestrate.Batch{{TaskName: "restored", Workflow: &orchestrate.Workflow{Assignment: child.batch.Workflow.Assignment, Phase: orchestrate.PhaseQueuedReview}}}
	u.orchestrationRoster()
	rows = append(rows, u.agents.nativeRoster(100, 4, now, true)...)
	if u.agents.hasLiveStatus() {
		t.Fatal("restored workflow restarted animation")
	}
	// A bare native working status uses the same pulse before activity arrives.
	v.agents.agents = []activityPaneAgent{{Name: "/root/worker", Responding: true}}
	v.agents.orchestration = nil
	a := v.agents.agentState(v.agents.agents[0], 30, now)
	b := v.agents.agentState(v.agents.agents[0], 30, now.Add(time.Second))
	if a == b || ansi.Strip(a) != "Working" || !v.agents.hasLiveStatus() {
		t.Fatal("bare working status did not breathe")
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/orchestration-roster-lifecycle.txt", rows, 100)
}

func TestUISnapshotOrchestrationRosterRestoredError(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	now := time.Unix(0, 0)
	u.clock = func() time.Time { return now }
	u.view.clock, u.agents.clock = u.clock, u.clock
	u.agents.painter.Theme = livediff.DarkTheme
	u.restoreMainHistory([]appServerHistoryTurn{{ID: "failed", Status: "failed"}}, nil, nil)
	// The root failure lives only in the restored Main transcript.
	if len(u.agents.entries) != 0 || u.agents.agentStatus(*u.session.agent("/root")) != '!' {
		t.Fatal("native root did not resolve its restored Main error")
	}
	u.ensureOrchestrationNavigation()
	u.orchestrationRoster()
	rows := u.agents.nativeRoster(100, 4, now, true)
	if !strings.Contains(ansi.Strip(rows[0]), "1 error") {
		t.Fatal("orchestration header lost the restored root error", rows[0])
	}
	rows = append(rows, "plain after roster")
	uisnapshot.AssertTerminal(t, "testdata/snapshots/orchestration-roster-restored-error.txt", rows, 100)
}
