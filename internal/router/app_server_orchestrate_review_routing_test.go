package router

import (
	"fmt"
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
		workflowArchive(t, u, w, thread, false)
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

func TestOrchestrateWorkflowPendingReviewAllowsNativeAncestry(t *testing.T) {
	for _, root := range []string{"main", "child"} {
		t.Run(root, func(t *testing.T) {
			u, w := workflowUI(t)
			workflowCompleted(t, u, "child", "implement", "")
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "native", "turn": map[string]any{"id": "native-turn"}})
			read, _ := workflowTake(t, u, w, "thread/read", "native", "")
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "native", "turnId": "native-turn", "item": map[string]any{"id": "read", "type": "commandExecution", "command": "cat native.go", "status": "completed"}})
			info := map[string]any{"id": "native", "parentThreadId": root, "agentNickname": "worker"}
			if root == "child" {
				delete(info, "parentThreadId")
				info["source"] = map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": root, "agent_path": "/root/worker"}}}
			}
			appServerTestNotify(t, u, "thread/started", map[string]any{"thread": info})
			orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"metadata not ready"}}`, read.ID))
			v := u.navigation.views[root]
			worker := v.session.agent("/root/worker")
			if worker == nil || !worker.Responding || len(u.navigation.pending)+len(u.navigation.reads)+len(u.navigation.unavailable) != 0 {
				t.Fatal("pending reviewer blocked confirmed native ancestry", worker, u.navigation.pending)
			}
			feed := strings.Join(v.agents.renderFeed(90, 60).lines, "\n")
			if strings.Count(feed, "native.go") != 1 {
				t.Fatal("confirmed native activity missing or duplicated", feed)
			}
		})
	}
}
