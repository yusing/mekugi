package router

import (
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
func workflowReviewerProfile(scratch string) string {
	return fmt.Sprintf("mekugi-review-%x", sha256.Sum256([]byte(scratch)))
}

func workflowReviewerPolicyConfirmed(raw []byte, scratch string) error {
	var settings appServerStatusConfig
	var profile struct {
		ID string `json:"id"`
	}
	if scratch == "" || json.Unmarshal(raw, &settings) != nil || json.Unmarshal(settings.PermissionProfile, &profile) != nil || profile.ID != workflowReviewerProfile(scratch) || string(settings.Approval) != `"never"` {
		return errors.New("host did not confirm the reviewer scratch-only permission profile and approval never")
	}
	return nil
}

func (u *appServerUI) workflowReviewerRequest(child *orchestrateChild, purpose orchestrate.WorkflowDispatchKind, settings configuredRoleSettings, scratch string) (map[string]any, error) {
	params, _, err := u.orchestrateThreadRequest(child.batch, orchestrateSpawnInput{})
	if err != nil {
		return nil, err
	}
	params["developerInstructions"] = workflowReviewerInstructions(purpose)
	settings.apply(params)
	delete(params, "sandbox")
	params["approvalPolicy"] = "never"
	profile := workflowReviewerProfile(scratch)
	params["permissions"] = profile
	if err := workflowReviewerStorageParams(params, child.batch.Checkout, scratch); err != nil {
		return nil, err
	}
	return params, nil
}

// Reapply storage policy on resume without changing the retained role settings.
func workflowReviewerStorageParams(params map[string]any, checkout, scratch string) error {
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(checkout, cache); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("reviewer cache must be outside the candidate checkout")
	}
	filesystem := map[string]any{":root": "read", scratch: "write"}
	filesystem[cache] = "write"
	filesystem[checkout] = "read"
	config := params["config"].(map[string]any)
	// Token-budget compaction bypasses local recovery. Retain this on resume.
	config["features.token_budget"] = false
	// Source: codex-rs/core/src/config/permissions.rs@ddb33e34b
	// Named profiles support scratch writes without making cwd writable. Legacy
	// workspaceWrite policies include cwd and cannot express this boundary.
	config["permissions."+workflowReviewerProfile(scratch)] = map[string]any{
		"filesystem": filesystem,
		"network":    map[string]any{"enabled": false},
	}
	config["shell_environment_policy.set"] = map[string]any{
		"TMPDIR": scratch, "TMP": scratch, "TEMP": scratch,
		// Run storage may itself have an unrelated ancestor repository.
		"GIT_CEILING_DIRECTORIES": scratch,
	}
	return nil
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
	w := batch.Workflow
	var text strings.Builder
	base := batch.Base
	delta := false
	for _, previous := range w.PreviousReviews {
		if previous.Result.Purpose == orchestrate.ReviewPurpose(purpose) && previous.Result.Verdict != orchestrate.VerdictBlocked {
			base = previous.Result.Revision
			delta = true
			text.WriteString("Correction review: inspect delta, unresolved findings/disputes and affected behavior. Reuse unchanged clear scope. Prior verdicts are context, not approval.\n")
			break
		}
	}
	text.WriteString("Review candidate " + w.Candidate.Revision + " against baseline " + base + " for " + string(purpose) + ".")
	reused := slices.ContainsFunc(w.Dispatches, func(d orchestrate.WorkflowDispatch) bool {
		return d.Kind == purpose && d.Generation != w.Generation && d.ThreadID != ""
	})
	// A context reset needs this purpose's retained evidence again.
	if slices.ContainsFunc(w.Dispatches, func(d orchestrate.WorkflowDispatch) bool {
		return d.Kind == purpose && d.Generation == w.Generation && d.Compaction != nil
	}) {
		reused = false
	}
	if purpose == orchestrate.DispatchSimplification {
		text.WriteString(" Review necessity and simplification; run checks only to resolve an equivalence gap.")
	}
	if !delta || !reused {
		text.WriteString(" Current accepted scope (replaces earlier scope): " + w.Assignment.Scope)
	}
	if !reused {
		text.WriteString("\nCandidate source is read-only. Temporary storage and the standard user cache are writable. Reuse exact-candidate runtime receipts; run checks only for a concrete evidence gap. Return the current-candidate JSON verdict as your final answer. Findings need stable IDs and concrete details; blocked needs a reason.")
	} else {
		if !delta {
			text.WriteString(" Review the retained accepted scope against the full baseline.")
		}
		text.WriteString(" Return the current-candidate JSON verdict as your final answer.")
	}
	for _, previous := range w.PreviousReviews {
		if reused || previous.Result.Purpose != orchestrate.ReviewPurpose(purpose) {
			continue
		}
		raw, _ := json.Marshal(&previous)
		text.WriteString("\nPrior review (retained verdict and dispatch provenance): " + string(raw))
	}
	for _, check := range w.Checks {
		if check.Revision == w.Candidate.Revision && check.Generation == w.Generation && check.Cwd == batch.Cwd {
			raw, _ := json.Marshal(check)
			text.WriteString("\nRuntime validation receipt (host command/exec): " + string(raw))
		}
	}
	if w.Report != "" {
		text.WriteString("\nImplementer report (claims, not host validation receipts):\n" + w.Report)
	}
	return text.String()
}
func workflowCorrectionAssignment(batch orchestrate.Batch) string {
	var text strings.Builder
	w := batch.Workflow
	text.WriteString("Correct or dispute the findings within the accepted scope. Follow the commit instructions and leave the checkout clean.\n\nAccepted scope: " + w.Assignment.Scope + "\n\nRejected because: " + w.Reason)
	writeWorkflowEvidence(&text, w)
	return text.String()
}

func (u *appServerUI) startWorkflowTurn(work *orchestrateReviewWork, text string) {
	if u.workflowWorkStopped(work) {
		u.failWorkflowDispatch(work, false, context.Canceled)
		return
	}
	params := map[string]any{"threadId": work.dispatch.ThreadID}
	if work.dispatch.Kind != orchestrate.DispatchCorrection {
		params["approvalPolicy"] = "never"
		params["permissions"] = workflowReviewerProfile(work.dispatch.Scratch)
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
	if r.method == "thread/compact/start" {
		work.acknowledged = true
		u.finishWorkflowWork(work)
		return
	}
	if r.method == "thread/resume" {
		var result struct {
			Thread appServerThreadInfo `json:"thread"`
		}
		if json.Unmarshal(m.Result, &result) != nil || result.Thread.ID != work.dispatch.ThreadID || result.Thread.Cwd != child.batch.Cwd {
			u.failWorkflowDispatch(work, true, errors.New("resumed reviewer identity changed"))
			return
		}
		if err := workflowReviewerPolicyConfirmed(m.Result, work.dispatch.Scratch); err != nil {
			u.failWorkflowDispatch(work, false, err)
			return
		}
		u.proxy.mu.Lock()
		u.navigation.views[work.dispatch.ThreadID].workflowReview.host = u.client
		u.proxy.mu.Unlock()
		u.prepareWorkflowReviewTurn(work)
		return
	}
	if r.method == "config/read" {
		role := u.proxy.orchestration.reviewers.Correctness
		if work.dispatch.Kind == orchestrate.DispatchSimplification {
			role = u.proxy.orchestration.reviewers.Simplification
		}
		u.orchestrateWork(func() func() {
			settings, err := resolveConfiguredRoleSettings(m.Result, role)
			var scratch string
			if err == nil {
				c := child.command
				scratch, err = u.proxy.orchestration.store.PrepareWorkflowReviewStorage(u.orchestrateStorageContext, c.workspace, c.main, child.batch.TaskName, work.dispatch.ID)
			}
			return func() {
				if err != nil {
					u.failWorkflowDispatch(work, false, err)
					return
				}
				work.dispatch.Scratch = scratch
				params, err := u.workflowReviewerRequest(child, work.dispatch.Kind, settings, scratch)
				if err != nil {
					u.failWorkflowDispatch(work, false, err)
					return
				}
				raw := jsontext.Value(mustMarshalJSON(params))
				c := child.command
				u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
					return s.RecordWorkflowReviewerParams(ctx, c.workspace, c.main, child.batch.TaskName, work.dispatch.ID, raw)
				}, func(err error) {
					if err != nil {
						u.failWorkflowDispatch(work, false, err)
						return
					}
					u.sendWorkflowRequest(work, "thread/start", params)
				})
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
		policyErr = workflowReviewerPolicyConfirmed(m.Result, work.dispatch.Scratch)
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
				u.proxy.mu.Lock()
				u.navigation.views[d.ThreadID].workflowReview.host = u.client
				u.proxy.mu.Unlock()
				u.startWorkflowTurn(work, workflowReviewAssignment(child.batch, d.Kind))
			}); err != nil {
				u.failWorkflowDispatch(work, false, err)
			}
			return
		}
		if work.completed != nil && work.completed.ID != d.TurnID {
			work.completed = nil
		}
		if work.messageTurn != d.TurnID {
			work.message, work.item, work.messageTurn = "", "", ""
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
		turn := event.TurnID
		if event.Turn.ID != "" {
			turn = event.Turn.ID
		}
		old := slices.ContainsFunc(child.batch.Workflow.Dispatches, func(d orchestrate.WorkflowDispatch) bool {
			return d.ID != work.dispatch.ID && d.ThreadID == event.ThreadID && (d.TurnID == turn || d.Compaction != nil && d.Compaction.TurnID == turn)
		})
		if old {
			return true // Retained history must not change the current turn's presentation.
		}
		if work.resetting {
			receipt := work.dispatch.Compaction
			if m.Method == "turn/started" && turn != "" {
				if receipt.TurnID != "" && receipt.TurnID != turn {
					u.failWorkflowDispatch(work, true, errors.New("another turn started during reviewer context reset"))
					return true
				}
				receipt.TurnID = turn
				if u.workflowWorkStopped(work) {
					_ = u.orchestrateCleanupInterrupt(child, event.ThreadID, turn)
				}
			}
			if turn != "" && turn != receipt.TurnID {
				return true
			}
			if m.Method == "item/completed" && turn != "" && event.Item.Type == "contextCompaction" {
				receipt.ItemID = event.Item.ID
			}
		} else if turn != "" && (work.dispatch.Compaction != nil && turn == work.dispatch.Compaction.TurnID || work.dispatch.TurnID != "" && turn != work.dispatch.TurnID) {
			return true
		}
		if turn == "" {
			return u.navigation == nil || u.navigation.views[event.ThreadID] == nil
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

// Reuse host settings and history, rather than resolving mutable role files again.
func (u *appServerUI) resumeWorkflowReviewer(work *orchestrateReviewWork) {
	c := work.child.command
	u.workflowStore(work.child, func(ctx context.Context, s *orchestrate.Store) error {
		_, err := s.PrepareWorkflowReviewStorage(ctx, c.workspace, c.main, work.child.batch.TaskName, work.dispatch.ID)
		return err
	}, func(err error) {
		if err != nil {
			u.failWorkflowDispatch(work, false, err)
			return
		}
		var saved jsontext.Value
		for _, d := range work.child.batch.Workflow.Dispatches {
			if d.ThreadID == work.dispatch.ThreadID && len(d.Params) != 0 {
				saved = d.Params
				break
			}
		}
		var params map[string]any
		if json.Unmarshal(saved, &params) != nil || params == nil {
			u.failWorkflowDispatch(work, false, errors.New("reviewer settings are unavailable for resume"))
			return
		}
		params["threadId"], params["excludeTurns"] = work.dispatch.ThreadID, true
		if err := workflowReviewerStorageParams(params, work.child.batch.Checkout, work.dispatch.Scratch); err != nil {
			u.failWorkflowDispatch(work, false, err)
			return
		}
		u.sendWorkflowRequest(work, "thread/resume", params)
	})
}

func (u *appServerUI) prepareWorkflowReviewTurn(work *orchestrateReviewWork) {
	if work.dispatch.Compaction != nil {
		work.resetting = true
		// The ordinary /compact RPC enters the reviewer's enforced local
		// journal reset. Missing host or local recovery capability blocks it.
		u.sendWorkflowRequest(work, "thread/compact/start", map[string]any{"threadId": work.dispatch.ThreadID})
		return
	}
	u.startWorkflowTurn(work, workflowReviewAssignment(work.child.batch, work.dispatch.Kind))
}
