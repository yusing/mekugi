package router

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestOrchestrateWorkflowScopeAmendmentConsumers(t *testing.T) {
	u, wire := workflowScopeReady(t)
	child := u.orchestrateThreads["child"]
	old := child.batch.Workflow.Assignment.Scope
	follow := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "amend", reviewScope: "Attribution only; exclude transport redesign", input: orchestrateSpawnInput{Message: "continue"}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(follow)
	drainOrchestrateWork(t, u)
	start, _ := workflowTake(t, u, wire, "turn/start", "child", "")
	if !strings.Contains(start.Params.Input[0].Text, follow.reviewScope) || child.batch.Workflow.Assignment.Scope != follow.reviewScope || len(child.batch.Workflow.Reviews) != 0 {
		t.Fatal("amendment did not reach the implementer or invalidate approval")
	}
	orchestrateTestReply(t, u, start, `{"turn":{"id":"amended"}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"amended","status":"inProgress"}}}`)
	workflowCompleted(t, u, "child", "amended", "")
	// Both already-reused reviewers must receive the current full scope.
	for _, kind := range []orchestrate.WorkflowDispatchKind{orchestrate.DispatchCorrectness, orchestrate.DispatchSimplification} {
		thread := child.workflowWork[kind].dispatch.ThreadID
		fresh := &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
		batches, err := fresh.Snapshot(u.session.cwd, "main")
		if err != nil {
			t.Fatal(err)
		}
		batch := batches[0]
		reset := workflowReviewerReset{run: journalRun{Directory: fresh.Directory, Workspace: u.session.cwd, Main: "main"}, batch: batch.TaskName}
		recovery, err := reset.recovery(thread, batch.Cwd)
		if err != nil || !strings.Contains(recovery, follow.reviewScope) || strings.Contains(recovery, old) || strings.Contains(recovery, "Correction review:") {
			t.Fatal("restart/reset recovered stale review scope", recovery, err)
		}
		// A fresh reviewer uses the same authoritative scope, with full rubric.
		batch.Workflow.Dispatches = nil
		if assignment := workflowReviewAssignment(batch, kind); !strings.Contains(assignment, follow.reviewScope) || !strings.Contains(assignment, "Candidate source is read-only") || strings.Contains(assignment, old) {
			t.Fatal("fresh reviewer lost amended scope", assignment)
		}
		workflowReview(t, u, wire, orchestrate.ReviewPurpose(kind), orchestrate.VerdictFindings)
	}
	correction, _ := workflowTake(t, u, wire, "turn/start", "child", orchestrate.DispatchCorrection)
	if text := correction.Params.Input[0].Text; !strings.Contains(text, follow.reviewScope) || strings.Contains(text, old) {
		t.Fatal("correction received stale scope", text)
	}
}

func workflowScopeReady(t *testing.T) (*appServerUI, *appServerTestInput) {
	t.Helper()
	u, w := workflowUI(t)
	workflowCompleted(t, u, "child", "implement", "")
	workflowReview(t, u, w, orchestrate.ReviewCorrectness, orchestrate.VerdictClear)
	workflowReview(t, u, w, orchestrate.ReviewSimplification, orchestrate.VerdictClear)
	workflowNotice(t, u, w)
	return u, w
}

func TestOrchestrateWorkflowScopeLaunchReplay(t *testing.T) {
	u, w := workflowScopeReady(t)
	c := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "amend", reviewScope: "new accepted scope", input: orchestrateSpawnInput{Message: "continue"}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(c)
	drainOrchestrateWork(t, u)
	req, _ := workflowTake(t, u, w, "turn/start", "child", "")
	orchestrateTestReply(t, u, req, `{"turn":{"id":"amended"}}`)
	if got := <-c.reply; got.err != nil {
		t.Fatal(got.err)
	}
	replay := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", tool: "spawn_agent", input: orchestrateSpawnInput{TaskName: "batch", Message: "bounded implementation"}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(replay)
	drainOrchestrateWork(t, u)
	if got := <-replay.reply; got.err != nil {
		t.Fatalf("original spawn replay rejected after explicit scope amendment: %v", got.err)
	}
	if w.Len() != 0 || u.orchestrateThreads["child"].batch.Workflow.Assignment.Scope != c.reviewScope {
		t.Fatal("spawn replay restarted work or replaced amended authority")
	}
	for _, input := range []orchestrateSpawnInput{
		{TaskName: "batch", Message: c.reviewScope},
		{TaskName: "batch", Message: "bounded implementation", Kind: orchestrate.WorkflowDiagnosis},
		{TaskName: "batch", Message: "bounded implementation", RequiredChecks: []string{"extra check"}},
	} {
		replay.input = input
		u.startOrchestratedChild(replay)
		drainOrchestrateWork(t, u)
		if got := <-replay.reply; got.err == nil {
			t.Fatal("changed launch request accepted", input)
		}
	}
}

func TestOrchestrateWorkflowScopeCanceledNextInput(t *testing.T) {
	u, w := workflowScopeReady(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &orchestrateCommand{ctx: ctx, workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "amend", reviewScope: "attribution only; exclude transport redesign", input: orchestrateSpawnInput{Message: "Do not replay this canceled instruction"}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(c)
	select {
	case complete := <-u.orchestrateCompletions:
		cancel()
		u.completeOrchestrateWork(complete)
	case <-time.After(5 * time.Second):
		t.Fatal("persistence timed out")
	}
	drainOrchestrateWork(t, u)
	if got := <-c.reply; got.delivery == nil || got.delivery.State != "canceled" {
		t.Fatalf("expected known-unsent amendment delivery, got %+v", got)
	}
	if u.orchestrateThreads["child"].batch.Workflow.Assignment.Scope != c.reviewScope {
		t.Fatal("amendment not retained")
	}
	u.proxy.orchestration.store = &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
	next := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "continue", input: orchestrateSpawnInput{Message: "Resume the accepted assignment"}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(next)
	drainOrchestrateWork(t, u)
	req, _ := workflowTake(t, u, w, "turn/start", "child", "")
	if text := req.Params.Input[0].Text; !strings.Contains(text, c.reviewScope) || strings.Contains(text, c.input.Message) {
		t.Fatalf("implementer starts with stale scope context after canceled amendment; actual input: %q", text)
	}
}

func TestOrchestrateWorkflowScopeUncertainAmendment(t *testing.T) {
	u, w := workflowScopeReady(t)
	c := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "amend", reviewScope: "first amended scope", input: orchestrateSpawnInput{Message: "continue"}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(c)
	drainOrchestrateWork(t, u)
	req, _ := workflowTake(t, u, w, "turn/start", "child", "")
	orchestrateTestReply(t, u, req, `{}`)
	if got := <-c.reply; got.delivery == nil || got.delivery.State != "uncertain" {
		t.Fatalf("fixture should have unconfirmed host delivery: %+v", got)
	}
	next := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "another-amendment", reviewScope: "second amended scope", input: orchestrateSpawnInput{Message: "continue again"}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(next)
	drainOrchestrateWork(t, u)
	if got := <-next.reply; got.err == nil || w.Len() != 0 {
		t.Fatal("unconfirmed delivery allowed another host request", got.err, w.String())
	}
	if child := u.orchestrateThreads["child"]; child.batch.Workflow.Assignment.Scope == next.reviewScope {
		t.Fatalf("scope changed without inspecting previous uncertain host input; wire: %s", w.String())
	}
}

func TestOrchestrateWorkflowScopeSettledUncertainDelivery(t *testing.T) {
	for _, mode := range []string{"live", "restart", "steer"} {
		t.Run(mode, func(t *testing.T) {
			u, wire := workflowScopeReady(t)
			c := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "uncertain", reviewScope: "first amended scope", input: orchestrateSpawnInput{Message: "continue"}, reply: make(chan orchestrateResult, 1)}
			turn := "child-turn"
			if mode == "steer" {
				start := *c
				start.callID, start.reviewScope = "start", ""
				u.startOrchestratedChild(&start)
				drainOrchestrateWork(t, u)
				request, _ := workflowTake(t, u, wire, "turn/start", "child", "")
				orchestrateTestReply(t, u, request, `{"turn":{"id":"child-turn"}}`)
				<-start.reply
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": turn}})
				drainOrchestrateWork(t, u)
				c.tool, c.reviewScope = "send_message", ""
			}
			u.startOrchestratedChild(c)
			drainOrchestrateWork(t, u)
			method := "turn/start"
			if mode == "steer" {
				method = "turn/steer"
			}
			request, _ := workflowTake(t, u, wire, method, "child", "")
			orchestrateTestReply(t, u, request, `{}`)
			receipt := <-c.reply
			if receipt.err == nil || receipt.delivery.State != "uncertain" {
				t.Fatal("fixture did not retain uncertain delivery", receipt)
			}
			if mode != "steer" {
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": turn}})
				drainOrchestrateWork(t, u)
			}
			if mode == "restart" {
				workflowRestart(t, u)
				workflowThreadRead(t, u, wire, "child", "completed", "")
				child := u.orchestrateThreads["child"]
				child.workflowDeferred = false
				u.driveOrchestrateWorkflows()
				drainOrchestrateWork(t, u)
			} else {
				workflowCompleted(t, u, "child", turn, "")
			}
			if mode != "steer" { // Changed scope needs reviews; unchanged approved input reuses them.
				workflowReview(t, u, wire, orchestrate.ReviewCorrectness, orchestrate.VerdictClear)
				workflowReview(t, u, wire, orchestrate.ReviewSimplification, orchestrate.VerdictClear)
			}
			workflowNotice(t, u, wire)
			store := &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
			retained, _, err := store.BeginDelivery(t.Context(), u.session.cwd, "main", orchestrate.Delivery{ID: c.callID, From: "main", Target: "child", Message: c.input.Message, ReviewScope: c.reviewScope, Deferred: mode == "steer"})
			if err != nil || retained.State != "uncertain" || retained.Error != receipt.delivery.Error || retained.TurnID != "" || retained.ObservedTurnID != turn || retained.ObservedStatus != "completed" {
				t.Fatal("settlement changed delivery provenance or lost host evidence", retained, err)
			}
			next := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", tool: "followup_task", callID: "next", reviewScope: "second amended scope", input: orchestrateSpawnInput{Message: "continue"}, reply: make(chan orchestrateResult, 1)}
			u.startOrchestratedChild(next)
			drainOrchestrateWork(t, u)
			request, _ = workflowTake(t, u, wire, "turn/start", "child", "")
			orchestrateTestReply(t, u, request, `{"turn":{"id":"next"}}`)
			if result := <-next.reply; result.err != nil {
				t.Fatal("settled host work blocked scope amendment", result.err)
			}
		})
	}
}
