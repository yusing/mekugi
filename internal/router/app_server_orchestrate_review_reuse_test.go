package router

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestWorkflowReviewerScratchFixtureDiscovery(t *testing.T) {
	u, wire := workflowUI(t)
	// Reproduce state-root nesting under an unrelated repository, independently
	// of the candidate's own worktree metadata.
	ancestor := u.proxy.orchestration.store.Directory
	gitTestRun(t, ancestor, "init", "--quiet")
	workflowCompleted(t, u, "child", "implement", "done")
	for _, kind := range []orchestrate.WorkflowDispatchKind{orchestrate.DispatchCorrectness, orchestrate.DispatchSimplification} {
		_, params := workflowTake(t, u, wire, "thread/start", "", kind)
		t.Run(string(kind), func(t *testing.T) {
			settings := params["config"].(map[string]any)["shell_environment_policy.set"].(map[string]any)
			for key, value := range settings {
				t.Setenv(key, value.(string))
			}
			for _, vcs := range []string{"shadow", "git"} {
				workspace := orchestrateVCSWorkspace(t, vcs)
				s := &orchestrate.Store{Directory: t.TempDir(), ShadowSnapshot: orchestrateShadowSnapshot}
				batch, err := s.Prepare(t.Context(), workspace, "fixture", "batch")
				if err != nil || (vcs == "shadow") != (batch.VCS == "shadow") {
					t.Fatal("review fixture inherited ancestor repository", vcs, batch, err)
				}
				if got, err := os.ReadFile(filepath.Join(batch.Cwd, "file")); err != nil || string(got) != "base" {
					t.Fatal("fixture preparation changed source bytes", err)
				}
			}
		})
	}
}

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
					if round == 3 && outcome == "completed" {
						follow.reviewScope = "Attribution only; exclude transport redesign"
					}
					u.startOrchestratedChild(follow)
					drainOrchestrateWork(t, u)
					start, _ := workflowTake(t, u, wire, "turn/start", "child", "")
					orchestrateTestReply(t, u, start, fmt.Sprintf(`{"turn":{"id":%q}}`, turn))
					appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": turn}})
					drainOrchestrateWork(t, u)
					// Context-budget coverage needs distinct candidates, not no-effect input.
					gitTestRun(t, child.batch.Cwd, "commit", "--allow-empty", "-qm", turn)
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
				_, recovery := deliverRecoveryCompaction(t, u.proxy, child.batch.Cwd, thread, "compact")
				if !strings.Contains(recovery, child.batch.Workflow.Assignment.Scope) || strings.Contains(recovery, "bounded implementation") {
					t.Fatal("local reset restored stale scope", recovery)
				}
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

func TestWorkflowUnchangedApprovedCandidate(t *testing.T) {
	u, wire := workflowUI(t, "required check")
	child := u.orchestrateThreads["child"]
	workflowCompleted(t, u, "child", "implement", "done")
	// Initial no-op baseline still requires checks and both runtime reviews.
	workflowCheck(t, u, wire, "required check", `{"exitCode":0,"stdout":"passed"}`)
	workflowReview(t, u, wire, orchestrate.ReviewCorrectness, orchestrate.VerdictClear)
	workflowReview(t, u, wire, orchestrate.ReviewSimplification, orchestrate.VerdictClear)
	workflowNotice(t, u, wire)
	prior := append([]orchestrate.WorkflowDispatch(nil), child.batch.Workflow.Dispatches...)
	for round := 0; round < 2; round++ {
		if round == 1 {
			u.proxy.journalCompaction = "auto"
			deliverRecoveryCompaction(t, u.proxy, child.batch.Cwd, "child", "no-effect-reset")
			workflowRestart(t, u) // Fresh state retains approval without live reviewers.
			child = u.orchestrateThreads["child"]
			n := u.ensureOrchestrationNavigation()
			n.resumes = map[string]*orchestrateResume{"child": {}}
			n.finishResume("child", nil) // Explicit host admission releases recovery's effect gate.
			drainOrchestrateWork(t, u)
		}
		turn := fmt.Sprintf("no-effect-%d", round)
		follow := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: turn, input: orchestrateSpawnInput{Message: "Confirm the result without edits"}, reply: make(chan orchestrateResult, 1)}
		if round == 0 {
			follow.reviewScope = child.batch.Workflow.Assignment.Scope // Equal effective scope preserves approval.
		}
		u.startOrchestratedChild(follow)
		drainOrchestrateWork(t, u)
		start, _ := workflowTake(t, u, wire, "turn/start", "child", "")
		orchestrateTestReply(t, u, start, fmt.Sprintf(`{"turn":{"id":%q}}`, turn))
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": turn}})
		drainOrchestrateWork(t, u)
		if round == 1 {
			writeTestFile(t, child.batch.Cwd+"/unclean.txt", "discard this temporary input")
		}
		workflowCompleted(t, u, "child", turn, "unchanged")
		if round == 1 {
			correction, _ := workflowTake(t, u, wire, "turn/start", "child", orchestrate.DispatchCorrection)
			orchestrateTestReply(t, u, correction, `{"turn":{"id":"restore-cleanliness"}}`)
			if err := os.Remove(child.batch.Cwd + "/unclean.txt"); err != nil {
				t.Fatal(err)
			}
			workflowCompleted(t, u, "child", "restore-cleanliness", "unchanged clean candidate")
		}
		if child.batch.Workflow.Phase != orchestrate.PhaseValidation {
			t.Fatal("unchanged candidate skipped required validation")
		}
		workflowCheck(t, u, wire, "required check", `{"exitCode":0,"stdout":"passed again"}`)
		if child.batch.Workflow.Phase != orchestrate.PhaseReady || len(child.batch.Workflow.PreviousReviews) != 2 || !reflect.DeepEqual(prior, child.batch.Workflow.Dispatches[:len(prior)]) {
			t.Fatalf("unchanged candidate lost readiness or original host evidence: phase=%s retained=%d", child.batch.Workflow.Phase, len(child.batch.Workflow.PreviousReviews))
		}
		workflowNotice(t, u, wire)
		if wire.Len() != 0 || len(child.workflowWork) != 0 {
			t.Fatal("unchanged approved candidate dispatched new review work", wire.String())
		}
		if _, err := u.proxy.orchestration.store.RecordIntegration(t.Context(), u.session.cwd, "main", "batch", "child"); err != nil {
			t.Fatal("retained approval did not reach integration gate", err)
		}
	}
}

func TestWorkflowChangedFollowupFullBaseline(t *testing.T) {
	u, wire := workflowUI(t)
	child := u.orchestrateThreads["child"]
	gitTestRun(t, child.batch.Cwd, "commit", "--allow-empty", "-qm", "initial candidate")
	workflowCompleted(t, u, "child", "implement", "done")
	workflowReview(t, u, wire, orchestrate.ReviewCorrectness, orchestrate.VerdictClear)
	workflowReview(t, u, wire, orchestrate.ReviewSimplification, orchestrate.VerdictClear)
	workflowNotice(t, u, wire)
	follow := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "new-input", input: orchestrateSpawnInput{Message: "Extend the implementation within the retained scope"}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(follow)
	drainOrchestrateWork(t, u)
	start, _ := workflowTake(t, u, wire, "turn/start", "child", "")
	orchestrateTestReply(t, u, start, `{"turn":{"id":"new-input"}}`)
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "new-input"}})
	drainOrchestrateWork(t, u)
	writeTestFile(t, child.batch.Cwd+"/followup.txt", "new implementation")
	workflowCompleted(t, u, "child", "new-input", "done")
	correction, _ := workflowTake(t, u, wire, "turn/start", "child", orchestrate.DispatchCorrection)
	orchestrateTestReply(t, u, correction, `{"turn":{"id":"commit-followup"}}`)
	gitTestCommit(t, child.batch.Cwd)
	workflowCompleted(t, u, "child", "commit-followup", "committed")
	for _, purpose := range []orchestrate.WorkflowDispatchKind{orchestrate.DispatchCorrectness, orchestrate.DispatchSimplification} {
		work := child.workflowWork[purpose]
		review, _ := workflowTake(t, u, wire, "turn/start", work.dispatch.ThreadID, purpose)
		assignment := review.Params.Input[0].Text
		if !strings.Contains(assignment, " against baseline "+child.batch.Base+" for "+string(purpose)) || strings.Contains(assignment, "Correction review:") {
			t.Fatalf("changed follow-up inherited delta-only %s coverage: %s", purpose, assignment)
		}
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
