package router

import (
	"slices"
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
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	v.orchestration = []activityPaneAgent{{Name: "/Orchestration/main"}, {Name: "/Orchestration/review_protocol", Responding: true}, {Name: "/Orchestration/active", Responding: true}}
	for i, age := range []time.Duration{2 * time.Second, 115 * time.Second, 28 * time.Second, 9 * time.Second, 9 * time.Second, 0} {
		agent := activityPaneAgent{Name: "/Orchestration/done" + string(rune('a'+i)), Final: true}
		if age != 0 {
			agent.Started, agent.LastResponse = now.Add(-time.Hour), now.Add(-age)
			agent.WorkTimer = activeWorkTimer{Known: true, ElapsedNS: int64(time.Minute)}
		}
		v.orchestration = append(v.orchestration, agent)
	}
	v.orchestrationSources = map[string]*liveActivityView{"/Orchestration/main": v}
	v.rosterSelected = "/Orchestration/doned"
	names := func() []string {
		var names []string
		for _, row := range v.nativeRosterRows() {
			if nativeThreadRow(row.agent.Name) {
				names = append(names, strings.TrimPrefix(row.agent.Name, "/Orchestration/"))
			}
		}
		return names
	}
	if got := names(); !slices.Equal(got, []string{"main", "review_protocol", "active", "donea", "doned", "donee", "donec", "doneb", "donef"}) {
		t.Fatal("roster lost recency, active order, or stable unknown/tied times", got)
	}
	var frames []string
	for _, native := range [][]activityPaneAgent{nil, {{Name: "/root/worker"}, {Name: "/root/another"}}} {
		v.agents = native
		frames = append(frames, v.nativeRoster(110, 10, now, true)...)
		frames = append(frames, v.nativeRoster(110, 5, now, true)...)
		if len(v.hits) < 2 || v.hits[0].agent != "/Orchestration/main" || v.hits[1].agent != "/Orchestration/review_protocol" {
			t.Fatal("working thread moved across overflow threshold", v.hits)
		}
	}
	// Completion moves the row ahead of older responses without changing selection.
	v.orchestration[1].Responding, v.orchestration[1].Final = false, true
	v.orchestration[1].LastResponse = now
	v.agents = nil
	frames = append(frames, v.nativeRoster(110, 10, now, true)...)
	if got := names(); !slices.Equal(got, []string{"main", "active", "review_protocol", "donea", "doned", "donee", "donec", "doneb", "donef"}) || v.rosterSelected != "/Orchestration/doned" {
		t.Fatal("completion changed selection identity or did not move by recency", got, v.rosterSelected)
	}
	ui := terminalUI{agents: v, main: &appServerUI{navigation: &orchestrateNavigation{}}}
	ui.handleNativeRosterKey("", 'j')
	if v.rosterSelected != "/Orchestration/donee" {
		t.Fatal("keyboard navigation used stale row position", v.rosterSelected)
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
