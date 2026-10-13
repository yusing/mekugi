package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestOrchestrateWorkflowEarlyReviewerStarts(t *testing.T) {
	u, w := workflowUI(t)
	workflowCompleted(t, u, "child", "implement", "")
	threads := []string{"019eaabb-correctness", "019eaabb-simplification"}
	purposes := []orchestrate.ReviewPurpose{orchestrate.ReviewCorrectness, orchestrate.ReviewSimplification}
	before := len(u.session.agents)
	for _, thread := range threads {
		appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": thread, "cwd": u.orchestrateThreads["child"].batch.Cwd}})
	}
	if len(u.session.agents) != before {
		t.Error("early reviewer starts created phantom native agents", u.session.agents)
	}
	for i, thread := range threads {
		workflowReviewThread(t, u, w, purposes[i], thread)
		workflowCompleted(t, u, thread, thread+"-turn", workflowVerdict(u.orchestrateThreads["child"], purposes[i]))
	}
	workflowNotice(t, u, w)
	if len(u.navigation.pending) != 0 || len(u.navigation.reads) != 0 || u.orchestrateBusy() {
		t.Fatal("reviewer starts stranded navigation after workflow completion", u.navigation.pending, u.navigation.reads)
	}
}

func TestOrchestrateWorkflowLateReviewerStarts(t *testing.T) {
	u, w := workflowUI(t)
	workflowCompleted(t, u, "child", "implement", "")
	threads := []string{"01a13480-correctness", "01a13480-simplification"}
	purposes := []orchestrate.ReviewPurpose{orchestrate.ReviewCorrectness, orchestrate.ReviewSimplification}
	for i, thread := range threads {
		workflowReviewThread(t, u, w, purposes[i], thread)
	}
	u.orchestrationRoster()
	for _, thread := range threads {
		appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": thread, "cwd": u.orchestrateThreads["child"].batch.Cwd}})
	}
	// Main publishes its native agent list again after reviewer admission.
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "next-main"}})
	rows := u.agents.nativeRosterRows()
	for _, thread := range threads {
		name := "/Orchestration/" + u.navigation.views[thread].workflowReview.name
		count := 0
		for _, row := range rows {
			if strings.Contains(row.agent.Name, thread) {
				t.Fatalf("reviewer %s published native alias %s", thread, row.agent.Name)
			}
			if row.agent.Name == name {
				count++
			}
		}
		if count != 1 || u.agents.orchestrationSources[name] != u.navigation.views[thread].agents {
			t.Fatalf("reviewer %s has %d independent rows or the wrong source", thread, count)
		}
	}
	if len(u.navigation.pending) != 0 || len(u.navigation.reads) != 0 {
		t.Fatal("acknowledged reviewer starts require no ancestry reads", u.navigation.pending, u.navigation.reads)
	}
}
