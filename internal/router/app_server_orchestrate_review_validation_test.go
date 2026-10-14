package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestOrchestrateWorkflowValidationRestart(t *testing.T) {
	for _, state := range []string{"unsent", "possibly-executed", "completed"} {
		t.Run(state, func(t *testing.T) {
			u, wire := workflowUI(t, "printf first", "printf second")
			child := u.orchestrateThreads["child"]
			child.workflowDeferred = state == "unsent"
			workflowCompleted(t, u, "child", "implement", "")
			revision := child.batch.Workflow.Candidate.Revision
			switch state {
			case "possibly-executed":
				// The host received the command, but its result never reached storage.
				workflowTake(t, u, wire, "command/exec", "", workflowValidation)
				u.reconcileWorkflow(child)
				drainOrchestrateWork(t, u)
				if child.batch.Workflow.Phase != orchestrate.PhaseValidation || child.workflowWork[workflowValidation] == nil {
					t.Fatal("reconciliation interrupted a live check")
				}
			case "completed":
				// Retain the first receipt, then restart before sending the second check.
				child.workflowDeferred = true
				workflowCheck(t, u, wire, "printf first", `{"exitCode":0}`)
			}
			workflowRestart(t, u)
			drainOrchestrateWork(t, u)
			child = u.orchestrateThreads["child"]
			if wire.Len() != 0 {
				t.Fatal("discovery dispatched host work", wire.String())
			}
			n := u.ensureOrchestrationNavigation()
			n.resumes = map[string]*orchestrateResume{"child": {}}
			n.finishResume("child", nil)
			drainOrchestrateWork(t, u)
			if state == "possibly-executed" {
				if child.batch.Workflow.Phase != orchestrate.PhaseNeedsDecision || strings.Contains(wire.String(), `"method":"command/exec"`) {
					t.Fatal("restart repeated a check with uncertain effects", child.batch.Workflow, wire.String())
				}
				if child.batch.Workflow.Candidate.Revision != revision || len(child.batch.Workflow.Checks) != 0 {
					t.Fatal("uncertain dispatch changed the candidate or manufactured a receipt")
				}
				workflowNotice(t, u, wire)
				return
			}
			if state == "unsent" {
				workflowCheck(t, u, wire, "printf first", `{"exitCode":0}`)
			}
			workflowCheck(t, u, wire, "printf second", `{"exitCode":0}`)
			workflowReview(t, u, wire, orchestrate.ReviewCorrectness, orchestrate.VerdictClear)
			workflowReview(t, u, wire, orchestrate.ReviewSimplification, orchestrate.VerdictClear)
			if child.batch.Workflow.Phase != orchestrate.PhaseReady || len(child.batch.Workflow.Checks) != 2 {
				t.Fatal("successful validation did not continue to readiness", child.batch.Workflow)
			}
			for i, check := range child.batch.Workflow.Checks {
				if check.Revision != revision || check.Cwd != child.batch.Cwd || check.CheckID != child.batch.Workflow.Assignment.RequiredChecks[i].ID {
					t.Fatal("retained check identity changed", check)
				}
			}
			workflowNotice(t, u, wire)
		})
	}
}
