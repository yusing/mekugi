package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

func readOnlyWorkflowParams(params map[string]any) jsontext.Value {
	delete(params, "permissions")
	params["sandbox"] = "read-only"
	params["approvalPolicy"] = "never"
	return jsontext.Value(`{"type":"readOnly","networkAccess":false}`)
}
func workflowReadOnlyConfirmed(raw []byte) error {
	var settings appServerStatusConfig
	if json.Unmarshal(raw, &settings) != nil || settings.Sandbox.Type != "readOnly" || string(settings.Approval) != `"never"` {
		return errors.New("host did not confirm read-only permissions and approval never")
	}
	return nil
}
func (u *appServerUI) workflowReviewerRequest(child *orchestrateChild, purpose orchestrate.WorkflowDispatchKind, settings workflowReviewerSettings) (map[string]any, error) {
	params, _, err := u.orchestrateThreadRequest(child.batch, orchestrateSpawnInput{})
	if err != nil {
		return nil, err
	}
	params["developerInstructions"] = workflowReviewerInstructions(purpose)
	if settings.DeveloperInstructions != nil {
		params["developerInstructions"] = *settings.DeveloperInstructions
	}
	if settings.Model != nil {
		params["model"] = *settings.Model
	}
	if settings.ReasoningEffort != nil {
		params["config"].(map[string]any)["model_reasoning_effort"] = *settings.ReasoningEffort
	}
	if settings.ServiceTier != nil {
		params["serviceTier"] = nil
		if *settings.ServiceTier != "" {
			params["serviceTier"] = *settings.ServiceTier
		}
	}
	readOnlyWorkflowParams(params)
	return params, nil
}

// Main owns these reviewer rubrics. Configured developer instructions replace them.
func workflowReviewerInstructions(purpose orchestrate.WorkflowDispatchKind) string {
	if purpose == orchestrate.DispatchCorrectness {
		return "Review the candidate against the accepted scope and affected existing behavior. Identify evidenced correctness, security, reliability, performance, and validation defects. For each finding, give the trigger, impact, source location, and smallest correction. Distinguish pre-existing issues from candidate regressions and missing runtime evidence from verified behavior."
	}
	return "Review the necessity of every change against the accepted scope and affected existing behavior. Identify removable or reducible code, tests, documents, configuration, and duplicate mechanisms. Propose the smallest evidence-backed simplification that preserves required behavior. Passing tests alone do not establish necessity. State evidence gaps where equivalence is unverified."
}

// Main owns final wording review of these per-turn assignments.
func workflowReviewAssignment(batch orchestrate.Batch, purpose orchestrate.WorkflowDispatchKind) string {
	return "Review candidate " + batch.Workflow.Candidate.Revision + " against baseline " + batch.Base + " for " + string(purpose) + ". Scope: " + batch.Workflow.Assignment.Scope + "\nInspect read-only. Return only the required JSON verdict for this revision and purpose. Findings need stable IDs and concrete details. If missing evidence prevents completing the review, return blocked with a reason. Otherwise keep reason empty. Return the JSON verdict as your final answer."
}
func workflowCorrectionAssignment(batch orchestrate.Batch) string {
	var text strings.Builder
	w := batch.Workflow
	text.WriteString("Correct the rejected result within the accepted scope, or dispute a review finding with evidence. Commit corrections linearly and leave the checkout clean. Scope: " + w.Assignment.Scope + "\nRejected because: " + w.Reason)
	writeWorkflowEvidence(&text, w)
	return text.String()
}

func (u *appServerUI) startWorkflowTurn(work *orchestrateReviewWork) {
	child := work.child
	if u.workflowWorkStopped(work) {
		u.failWorkflowDispatch(work, false, context.Canceled)
		return
	}
	text := ""
	params := map[string]any{"threadId": work.dispatch.ThreadID}
	switch work.dispatch.Kind {
	case orchestrate.DispatchCorrection:
		text = workflowCorrectionAssignment(child.batch)
	default:
		text = workflowReviewAssignment(child.batch, work.dispatch.Kind)
		params["approvalPolicy"] = "never"
		params["sandboxPolicy"] = jsontext.Value(`{"type":"readOnly","networkAccess":false}`)
		params["outputSchema"] = jsontext.Value(orchestrate.ReviewResultSchema)
	}
	// Correction keeps the implementer's current thread policy.
	params["input"] = appserver.Input(text)
	u.sendWorkflowRequest(work, "turn/start", params)
}

func (u *appServerUI) workflowResponse(r orchestrateRPC, m appserver.Message) {
	work, child := r.review, r.child
	switch r.method {
	case "thread/read":
		u.workflowRecoveryResponse(work, m)
		return
	case "command/exec":
		u.workflowCheckResponse(work, m)
		return
	case "command/exec/terminate":
		if m.Error != nil {
			u.setNotice("Workflow check termination: "+m.Error.Message, true)
		}
		return
	}
	if m.Error != nil {
		u.failWorkflowDispatch(work, false, fmt.Errorf("%s: %s", r.method, m.Error.Message))
		return
	}
	if r.method == "config/read" {
		role := u.proxy.orchestration.reviewers.Correctness
		if work.dispatch.Kind == orchestrate.DispatchSimplification {
			role = u.proxy.orchestration.reviewers.Simplification
		}
		u.orchestrateWork(func() func() {
			settings, err := resolveWorkflowReviewerSettings(m.Result, role)
			return func() {
				if err != nil {
					u.failWorkflowDispatch(work, false, err)
					return
				}
				params, err := u.workflowReviewerRequest(child, work.dispatch.Kind, settings)
				if err != nil {
					u.failWorkflowDispatch(work, false, err)
					return
				}
				u.sendWorkflowRequest(work, "thread/start", params)
			}
		})
		return
	}
	var result struct {
		Thread appServerThreadInfo `json:"thread"`
		Turn   appServerTurn       `json:"turn"`
	}
	if err := json.Unmarshal(m.Result, &result); err != nil {
		u.failWorkflowDispatch(work, true, err)
		return
	}
	var policyErr error
	if r.method == "thread/start" {
		if result.Thread.ID == "" {
			u.failWorkflowDispatch(work, true, errors.New("review thread identity is unavailable"))
			return
		}
		work.dispatch.ThreadID = result.Thread.ID
		policyErr = workflowReadOnlyConfirmed(m.Result)
		if result.Thread.Cwd != child.batch.Cwd {
			policyErr = errors.New("review thread cwd mismatch")
		}
	} else {
		if result.Turn.ID == "" {
			u.failWorkflowDispatch(work, true, errors.New("workflow turn identity is unavailable"))
			return
		}
		work.dispatch.TurnID = result.Turn.ID
		if u.workflowWorkStopped(work) {
			_ = u.orchestrateCleanupInterrupt(child, work.dispatch.ThreadID, work.dispatch.TurnID)
		}
	}
	c, d := child.command, work.dispatch
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		return s.RecordWorkflowDispatch(ctx, c.workspace, c.main, child.batch.TaskName, d.ID, d.ThreadID, d.TurnID)
	}, func(err error) {
		if err != nil {
			if d.TurnID != "" {
				_ = u.orchestrateCleanupInterrupt(child, d.ThreadID, d.TurnID)
			}
			u.workflowFailure(child, orchestrate.PhaseNeedsDecision, err)
			return
		}
		if r.method == "thread/start" {
			if result.Thread.Cwd != child.batch.Cwd {
				// Rejected metadata must not admit a foreign workspace or leave
				// its early notification waiting for a view we cannot register.
				if n := u.navigation; n != nil {
					n.pending = slices.DeleteFunc(n.pending, func(m appserver.Message) bool {
						var event appServerEvent
						_ = json.Unmarshal(m.Params, &event)
						return event.ThreadID == d.ThreadID || event.Thread.ID == d.ThreadID
					})
				}
				u.failWorkflowDispatch(work, false, policyErr)
				return
			}
			if u.proxy.usage != nil {
				u.proxy.usage.markNew(d.ThreadID)
			}
			if err := u.addWorkflowReviewView(child, d, m.Result, false, func() {
				if policyErr != nil {
					u.failWorkflowDispatch(work, false, policyErr)
					return
				}
				u.startWorkflowTurn(work)
			}); err != nil {
				u.failWorkflowDispatch(work, false, err)
			}
			return
		}
		work.acknowledged = true
		if work.completed == nil && d.ThreadID == child.batch.Launch.ThreadID {
			child.turn = d.TurnID
		}
		u.finishWorkflowWork(work)
	})
}

// Reviewer events are private to the workflow, not separate batch finals. The
// implementer continues through the existing view/lifecycle owner below this hook.
func (u *appServerUI) workflowReviewerMessage(m appserver.Message) bool {
	if m.Method == "" {
		return false
	}
	var event appServerEvent
	if json.Unmarshal(m.Params, &event) != nil {
		return false
	}
	if event.Thread.ID != "" {
		event.ThreadID = event.Thread.ID
	}
	if event.ThreadID == "" {
		return false
	}
	for _, child := range u.orchestrateThreads {
		if child.batch.Launch.ThreadID == event.ThreadID {
			continue
		}
		var work *orchestrateReviewWork
		for _, candidate := range child.workflowWork {
			if candidate.dispatch.ThreadID == event.ThreadID {
				work = candidate
			}
		}
		if work == nil {
			if child.batch.Workflow != nil && slices.ContainsFunc(child.batch.Workflow.Dispatches, func(d orchestrate.WorkflowDispatch) bool {
				return d.ThreadID == event.ThreadID && (d.Kind == orchestrate.DispatchCorrectness || d.Kind == orchestrate.DispatchSimplification)
			}) {
				return u.navigation == nil || u.navigation.views[event.ThreadID] == nil
			}
			continue
		}
		if m.Method == "item/completed" && event.Item.Type == "agentMessage" && event.Item.Delivery != "async" {
			work.message, work.item, work.messageTurn = event.Item.Text, event.Item.ID, event.TurnID
		}
		if m.Method == "turn/completed" {
			work.completed = &event.Turn
			if event.Turn.Status == "failed" {
				detail := "Review failed"
				if event.Turn.Error != nil {
					detail = event.Turn.Error.Message
				}
				u.workflowReviewError(event.ThreadID, detail)
			}
			u.finishWorkflowWork(work)
		}
		if m.Method == "item/tool/requestUserInput" || m.Method == "item/commandExecution/requestApproval" || m.Method == "item/fileChange/requestApproval" {
			if len(m.ID) != 0 {
				_ = u.client.Respond(m.ID, map[string]any{"decision": "cancel"})
			}
			u.workflowFailure(child, orchestrate.PhaseNeedsDecision, errors.New("read-only review requested interactive input"))
			return true
		}
		return u.navigation == nil || u.navigation.views[event.ThreadID] == nil
	}
	return false
}

func (u *appServerUI) sendWorkflowRequest(work *orchestrateReviewWork, method string, params map[string]any) {
	if u.workflowWorkStopped(work) {
		u.failWorkflowDispatch(work, false, context.Canceled)
		return
	}
	if err := u.orchestrateRPCRequest(orchestrateRPC{method: method, child: work.child, review: work}, params); err != nil {
		u.failWorkflowDispatch(work, true, err)
	}
}
