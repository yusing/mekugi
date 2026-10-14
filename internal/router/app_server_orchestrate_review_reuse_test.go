package router

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestWorkflowReviewerCompactionAttribution(t *testing.T) {
	for _, outcome := range []string{"completed", "unsupported", "uncertain", "no-recovery"} {
		t.Run(outcome, func(t *testing.T) {
			u, wire := workflowUI(t)
			child := u.orchestrateThreads["child"]
			for round := 0; round < 4; round++ {
				turn := "implement"
				if round > 0 {
					turn = fmt.Sprintf("implementation-%d", round)
					follow := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: turn, input: orchestrateSpawnInput{Message: "Continue the same scope"}, reply: make(chan orchestrateResult, 1)}
					u.startOrchestratedChild(follow)
					drainOrchestrateWork(t, u)
					start, _ := workflowTake(t, u, wire, "turn/start", "child", "")
					orchestrateTestReply(t, u, start, fmt.Sprintf(`{"turn":{"id":%q}}`, turn))
					appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": turn}})
					drainOrchestrateWork(t, u)
				}
				workflowCompleted(t, u, "child", turn, "")
				if round == 3 {
					break
				}
				workflowReview(t, u, wire, orchestrate.ReviewCorrectness, orchestrate.VerdictClear)
				workflowReview(t, u, wire, orchestrate.ReviewSimplification, orchestrate.VerdictClear)
				workflowNotice(t, u, wire)
			}
			work := child.workflowWork[orchestrate.DispatchCorrectness]
			thread := work.dispatch.ThreadID

			compact, _ := workflowTake(t, u, wire, "thread/compact/start", thread, "")
			// A delayed verdict from this reused thread is never the new turn.
			workflowCompleted(t, u, thread, thread+"-turn", workflowVerdict(child, orchestrate.ReviewCorrectness))
			if work.completed != nil || len(child.batch.Workflow.Reviews) != 0 {
				t.Fatal("adopted an earlier review turn")
			}
			if outcome == "unsupported" {
				orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-32601,"message":"method unavailable"}}`, compact.ID))
				drainOrchestrateWork(t, u)
				if !workflowTerminal(child.batch.Workflow) || !strings.Contains(child.batch.Workflow.Reason, "method unavailable") || strings.Contains(wire.String(), `"method":"turn/start"`) {
					t.Fatal("unsupported compaction launched another review")
				}
				return
			}
			if outcome == "uncertain" {
				workflowRestart(t, u)
				drainOrchestrateWork(t, u)
				workflowThreadRead(t, u, wire, thread, "completed", workflowVerdict(child, orchestrate.ReviewCorrectness))
				if u.orchestrateThreads["child"].batch.Workflow.Phase != orchestrate.PhaseNeedsDecision || strings.Contains(wire.String(), `"method":"turn/start"`) {
					t.Fatal("recovery adopted an old turn or repeated an uncertain effect")
				}
				return
			}
			if outcome == "completed" {
				if err := u.proxy.journals.initialize(t.Context(), u.proxy.replayStore, child.batch.Cwd, thread, "/root", ""); err != nil {
					t.Fatal(err)
				}
				if err := u.proxy.journals.bindIdentity(t.Context(), u.proxy.replayStore, child.batch.Cwd, thread, "", "/root", true); err != nil {
					t.Fatal(err)
				}
				if _, err := u.proxy.journals.apply(t.Context(), u.proxy.replayStore, child.batch.Cwd, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Review evidence")}}); err != nil {
					t.Fatal(err)
				}
				deliverRecoveryCompaction(t, u.proxy, child.batch.Cwd, thread, "compact")
			}
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "compact"}})
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "compact", "item": map[string]any{"id": "summary", "type": "contextCompaction"}})
			workflowCompleted(t, u, thread, "compact", "")
			if strings.Contains(wire.String(), `"method":"turn/start"`) {
				t.Fatal("compaction completion overtook acknowledgement")
			}
			orchestrateTestReply(t, u, compact, `{}`)
			if outcome == "no-recovery" {
				if !strings.Contains(child.batch.Workflow.Reason, "no matching local recovery") || strings.Contains(wire.String(), `"method":"turn/start"`) {
					t.Fatal("host lifecycle bypassed local recovery", child.batch.Workflow.Reason)
				}
				return
			}
			start, _ := workflowTake(t, u, wire, "turn/start", thread, "")
			orchestrateTestReply(t, u, start, `{"turn":{"id":"current"}}`)
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "current"}})
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "current", "item": map[string]any{"id": "read", "type": "commandExecution", "command": "cat candidate.go"}})
			view := u.navigation.views[thread]
			agent := view.session.agent("/root")
			timer, started := agent.WorkTimer, view.turnStarted
			view.view.clock = func() time.Time { return started }
			feed := strings.Join(view.view.renderFeed(90, 60).lines, "\n")
			history := append([]orchestrate.WorkflowDispatch(nil), child.batch.Workflow.Dispatches...)
			if view.turn != "current" || !agent.Responding || timer.Since.IsZero() || !strings.Contains(feed, "candidate.go") {
				t.Fatalf("current review presentation did not start: turn=%q responding=%v timer=%+v feed=%q", view.turn, agent.Responding, timer, feed)
			}
			for _, old := range []string{thread + "-turn", "compact"} {
				for _, method := range []string{"turn/started", "turn/completed"} {
					appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turn": map[string]any{"id": old, "status": "completed"}})
					if view.turn != "current" || !agent.Responding || agent.WorkTimer != timer || view.turnStarted != started || strings.Join(view.view.renderFeed(90, 60).lines, "\n") != feed {
						t.Fatalf("stale %s for %s changed current reviewer presentation", method, old)
					}
				}
			}
			workflowCompleted(t, u, thread, thread+"-turn", workflowVerdict(child, orchestrate.ReviewCorrectness))
			if !reflect.DeepEqual(history, child.batch.Workflow.Dispatches) || work.completed != nil {
				t.Fatal("stale events changed retained evidence or current completion")
			}
			workflowCompleted(t, u, thread, "current", workflowVerdict(child, orchestrate.ReviewCorrectness))
			d := child.batch.Workflow.Dispatches[len(child.batch.Workflow.Dispatches)-2]
			if d.ThreadID != thread || d.TurnID != "current" || d.ResultItemID != "current-result" || d.Compaction == nil || d.Compaction.TurnID != "compact" || d.Compaction.ItemID != "summary" {
				t.Fatal("lost exact compaction or verdict attribution", d)
			}
		})
	}
}

func TestWorkflowReviewScratchCleanupAfterFailedResume(t *testing.T) {
	u, wire := workflowUI(t)
	workflowCompleted(t, u, "child", "implement", "done")
	workflowReview(t, u, wire, orchestrate.ReviewCorrectness, orchestrate.VerdictClear)
	workflowReview(t, u, wire, orchestrate.ReviewSimplification, orchestrate.VerdictClear)
	workflowNotice(t, u, wire)
	child := u.orchestrateThreads["child"]
	var scratch []string
	for _, d := range child.batch.Workflow.Dispatches {
		if d.Scratch != "" {
			scratch = append(scratch, d.Scratch)
		}
	}
	u.navigation.resumes = map[string]*orchestrateResume{"child": {}}
	u.navigation.finishResume("child", errors.New("host rejected batch resume"))
	if u.orchestrateThreads["child"] != nil || len(scratch) != 2 {
		t.Fatal("fixture did not remove the resumed batch controller")
	}
	if err := u.clearSessionPresentation(); err != nil {
		t.Fatal(err)
	}
	u.thread = "another-main"
	u.session.start(u.thread, t.TempDir())
	if err := u.releaseWorkflowReviewStorage(); err != nil {
		t.Fatal(err)
	}
	for _, path := range scratch {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("review scratch survived final cleanup", path, err)
		}
	}
}

func TestWorkflowReviewScratchHistoryOnlyShutdown(t *testing.T) {
	u, wire := workflowUI(t)
	workflowCompleted(t, u, "child", "implement", "done")
	workflowReview(t, u, wire, orchestrate.ReviewCorrectness, orchestrate.VerdictClear)
	workflowReview(t, u, wire, orchestrate.ReviewSimplification, orchestrate.VerdictClear)
	workflowNotice(t, u, wire)
	batches, err := u.proxy.orchestration.store.Snapshot(u.session.cwd, u.thread)
	if err != nil {
		t.Fatal(err)
	}
	owner := u.client
	observer, _ := newAppServerTestUI()
	u.client = observer.client
	u.recoverOrchestratedWorkflows(batches)
	drainOrchestrateWork(t, u)
	if err := u.releaseWorkflowReviewStorage(); err != nil {
		t.Fatal(err)
	}
	for _, d := range batches[0].Workflow.Dispatches {
		if d.Scratch != "" {
			if _, err := os.Stat(d.Scratch); err != nil {
				t.Fatal("history-only host removed another host's review scratch", err)
			}
		}
	}
	u.client = owner
	if err := u.releaseWorkflowReviewStorage(); err != nil {
		t.Fatal(err)
	}
	for _, d := range batches[0].Workflow.Dispatches {
		if d.Scratch != "" {
			if _, err := os.Stat(d.Scratch); !os.IsNotExist(err) {
				t.Fatal("confirmed owner shutdown retained review scratch", err)
			}
		}
	}
}
