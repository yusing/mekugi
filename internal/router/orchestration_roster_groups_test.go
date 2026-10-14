package router

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/orchestrate"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotOrchestrationRosterReviewerGroups(t *testing.T) {
	u, w := workflowUI(t)
	workflowActiveReviews(t, u, w)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, v := range u.navigation.views {
		v.clock = func() time.Time { return now }
		v.view.clock, v.agents.clock = v.clock, v.clock
		v.agents.painter.Theme = livediff.DarkTheme
		v.modelsLoading = true
		a := v.session.agent("/root")
		a.InputTokens, a.OutputTokens, a.Cost = 1000, 100, .01
		a.TokensKnown, a.CostKnown = true, true
	}
	for _, thread := range []string{"reviewer-c", "reviewer-s"} {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": thread + "-turn"}})
	}
	snapshot := func(name string, width, limit int) {
		t.Helper()
		v := u.viewedUI()
		v.orchestrationRoster()
		lines := v.agents.nativeRoster(width, limit, now, true)
		uisnapshot.Assert(t, "testdata/snapshots/orchestration-reviewer-groups-"+name+".txt", strings.Join(lines, "\n")+"\n")
		count, _, _, usage := v.agents.nativeRosterTotals(v.agents.nativeRosterRows(), now)
		if count != 4 || usage.InputTokens != 4000 || usage.OutputTokens != 400 || usage.Cost != .04 {
			t.Fatal("group visibility changed whole-run accounting", count, usage)
		}
	}
	const parent = "/Orchestration/batch"
	const reviewer = parent + "/correctness"
	u.agents.rosterSelected, u.shell.focus = parent, 3
	snapshot("active", 110, 8)
	rows := u.agents.nativeRosterRows()
	if len(rows) != 4 || rows[1].agent.Name != parent || rows[2].agent.Name != reviewer || rows[2].depth != 1 || rows[3].depth != 1 {
		t.Fatal("reviewers did not nest under their active parent", rows)
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/orchestration-reviewer-groups-styles.txt", append(u.agents.nativeRoster(110, 8, now, true), "plain after roster"), 110)
	// Existing keyboard navigation opens the reviewer; thread switching keeps the group.
	if err := u.shell.send("j\r"); err != nil || u.viewedUI().thread != "reviewer-c" {
		t.Fatal("reviewer navigation failed", err)
	}
	u.switchOrchestratedThread("main")
	u.agents.rosterSelected = parent + "/simplification"
	snapshot("short", 70, 3)
	for i, thread := range []string{"reviewer-c", "reviewer-s"} {
		purpose := []orchestrate.ReviewPurpose{orchestrate.ReviewCorrectness, orchestrate.ReviewSimplification}[i]
		workflowCompleted(t, u, thread, thread+"-turn", workflowVerdict(u.orchestrateThreads["child"], purpose))
	}
	u.agents.rosterSelected = parent
	snapshot("idle-default", 110, 8)
	if slices.ContainsFunc(u.agents.nativeRosterRows(), func(row liveActivityRosterRow) bool { return row.agent.Name == reviewer }) {
		t.Fatal("idle parent did not collapse its reviewers")
	}
}
