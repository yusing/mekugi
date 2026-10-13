package router

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func workflowArchive(t *testing.T, u *appServerUI, w *appServerTestInput, thread string, fail bool) {
	t.Helper()
	r, _ := workflowTake(t, u, w, "thread/archive", thread, "")
	if fail {
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-32600,"message":"archive response lost"}}`, r.ID))
	} else {
		orchestrateTestReply(t, u, r, `{}`)
	}
}

func TestOrchestrateReviewerArchiveRetainsEvidenceAndUncertainty(t *testing.T) {
	u, w := workflowUI(t)
	workflowActiveReviews(t, u, w)
	child := u.orchestrateThreads["child"]
	u.proxy.usage = storedUsageFixture(u.proxy.replayStore)
	t.Cleanup(u.proxy.usage.close)
	u.proxy.usage.observation("reviewer-c", "reviewer-c", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10})
	workflowCompleted(t, u, "reviewer-c", "reviewer-c-turn", workflowVerdict(child, orchestrate.ReviewCorrectness))
	d := child.batch.Workflow.Dispatches[0]
	if d.Archive == nil || d.Archive.State != "dispatching" || d.ResultItemID == "" || d.HostStatus != "completed" {
		t.Fatal("archive preceded durable completion and verdict", d)
	}
	usage := storedUsageFixture(u.proxy.replayStore)
	t.Cleanup(usage.close)
	requireStoredUsage(t, usage, "reviewer-c", 10, 0, 1, .00002)
	workflowArchive(t, u, w, "reviewer-c", true)
	workflowCompleted(t, u, "reviewer-s", "reviewer-s-turn", workflowVerdict(child, orchestrate.ReviewSimplification))
	workflowArchive(t, u, w, "reviewer-s", false)
	workflowNotice(t, u, w)
	if child.batch.Workflow.Phase != orchestrate.PhaseReady || len(child.batch.Workflow.Reviews) != 2 {
		t.Fatal("archive failure changed review approval")
	}
	workflowRestart(t, u)
	if w.Len() != 0 {
		t.Fatal("recovery repeated an archive or resumed a reviewer", w.String())
	}
	child = u.orchestrateThreads["child"]
	if child.batch.Workflow.Dispatches[0].Archive.State != "uncertain" || child.batch.Workflow.Dispatches[1].Archive.State != "archived" {
		t.Fatal("recovery lost archive outcomes")
	}
	child.batch.Workflow.Generation++ // Older unconfirmed effects stay inspectable.
	rows, err := orchestrateListAgents([]orchestrate.Batch{child.batch}, orchestrateListInput{})
	raw, _ := json.Marshal(rows)
	if err != nil || !strings.Contains(string(raw), "archive response lost") {
		t.Fatal("summary hid archive uncertainty", err)
	}
}

func TestOrchestrateReviewerArchiveTerminalFailure(t *testing.T) {
	for _, status := range []string{"failed", "interrupted", "completed"} {
		t.Run(status, func(t *testing.T) {
			u, w := workflowUI(t)
			workflowActiveReviews(t, u, w)
			orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"reviewer-c","turn":{"id":"reviewer-c-turn","status":%q}}}`, status))
			workflowArchive(t, u, w, "reviewer-c", false)
			child := u.orchestrateThreads["child"]
			if child.batch.Workflow.Dispatches[0].HostStatus != status || child.batch.Workflow.Dispatches[1].Archive != nil || len(child.batch.Workflow.Reviews) != 0 || !workflowTerminal(child.batch.Workflow) {
				t.Fatal("failed review released active sibling or lost failure evidence")
			}
		})
	}
}

func TestOrchestrateReviewerArchivePreservesUnretainedUsageAndActiveDescendants(t *testing.T) {
	for _, obstacle := range []string{"usage", "descendant"} {
		t.Run(obstacle, func(t *testing.T) {
			u, w := workflowUI(t)
			workflowActiveReviews(t, u, w)
			child := u.orchestrateThreads["child"]
			if obstacle == "usage" {
				u.proxy.usage = newThreadUsage()
				u.proxy.usage.storageFailed = map[string]bool{"reviewer-c": true}
			} else {
				store, journals := u.proxy.replayStore, u.proxy.journals
				for _, identity := range []struct{ thread, parent, author string }{{"reviewer-c", "", "/root"}, {"native", "reviewer-c", "/root/native"}} {
					if err := journals.initialize(t.Context(), store, child.batch.Cwd, identity.thread, identity.author, identity.parent); err != nil {
						t.Fatal(err)
					}
					if err := journals.bindIdentity(t.Context(), store, child.batch.Cwd, identity.thread, identity.parent, identity.author, true); err != nil {
						t.Fatal(err)
					}
				}
				if err := journals.observeLifecycle(t.Context(), store, child.batch.Cwd, "native", "working", ""); err != nil {
					t.Fatal(err)
				}
			}
			workflowCompleted(t, u, "reviewer-c", "reviewer-c-turn", workflowVerdict(child, orchestrate.ReviewCorrectness))
			archive := child.batch.Workflow.Dispatches[0].Archive
			if strings.Contains(w.String(), "thread/archive") || archive == nil || archive.State != "not_sent" || archive.Error == "" || len(child.batch.Workflow.Reviews) != 1 {
				t.Fatal("unsafe archive or missing retained failure", archive, w.String())
			}
		})
	}
}
