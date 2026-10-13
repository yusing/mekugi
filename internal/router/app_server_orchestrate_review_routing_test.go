package router

import (
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
