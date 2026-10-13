package router

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/orchestrate"
)

// workflowValidation keys an in-flight host check. Checks have no durable
// dispatch; recovery reruns any check without a retained result.
const workflowValidation orchestrate.WorkflowDispatchKind = "validation"

// A work item owns one host effect. Durable dispatch and completion identities
// live in the store; these fields only serialize live event/ack arrival.
type orchestrateReviewWork struct {
	child                      *orchestrateChild
	dispatch                   orchestrate.WorkflowDispatch
	check                      orchestrate.RequiredCheck
	checkOutput                string
	checkUTF8                  [2]string // Incomplete stdout/stderr rune bytes, at most three each.
	acknowledged               bool
	completed                  *appServerTurn
	message, item, messageTurn string
	processing                 bool
	// lifecycleOnly repairs retained implementer observations, not workflow policy.
	lifecycleOnly bool
	// canceled marks work of a stopped round; a later resumed round never
	// adopts its late host responses.
	canceled bool
}

func implementationWorkflow(child *orchestrateChild) bool {
	return child != nil && child.batch.Workflow != nil && child.batch.Workflow.Assignment.Kind == orchestrate.WorkflowImplementation
}
func workflowTerminal(w *orchestrate.Workflow) bool {
	return w.Phase == orchestrate.PhaseReady || w.Phase == orchestrate.PhaseNeedsDecision || w.Phase == orchestrate.PhaseFailed || w.Phase == orchestrate.PhaseCanceled
}

// Runtime automation owns the implementer only while it drives a round. A
// stopped or ready workflow accepts new input, which starts a new round.
func workflowInputReserved(child *orchestrateChild) bool {
	if !implementationWorkflow(child) {
		return false
	}
	if child.workflowPending != 0 {
		return true
	}
	for _, work := range child.workflowWork {
		if !work.canceled {
			return true
		}
	}
	switch child.batch.Workflow.Phase {
	case orchestrate.PhaseImplementation:
		return child.workflowEnding
	case orchestrate.PhaseValidation, orchestrate.PhaseQueuedReview, orchestrate.PhaseReview, orchestrate.PhaseCorrection:
		return true
	}
	return false
}
func (u *appServerUI) workflowWorkStopped(work *orchestrateReviewWork) bool {
	return u.orchestrateClosing || work.canceled || work.child.workflowStopped || workflowTerminal(work.child.batch.Workflow)
}

// workflowResumable reports whether a started implementer turn is new input,
// rather than the runtime's own correction turn.
func workflowResumable(child *orchestrateChild, turn string) bool {
	if !implementationWorkflow(child) || !child.workflowStopped && !workflowTerminal(child.batch.Workflow) {
		return false
	}
	work := child.workflowWork[orchestrate.DispatchCorrection]
	return work == nil || work.canceled && work.dispatch.TurnID != "" && work.dispatch.TurnID != turn
}
func workflowResponding(child *orchestrateChild) bool {
	for _, work := range child.workflowWork {
		if work.completed == nil {
			return true
		}
	}
	return false
}

// All run mutations use the existing asynchronous, ordered storage queue.
func (u *appServerUI) workflowStore(child *orchestrateChild, apply func(context.Context, *orchestrate.Store) error, next func(error)) {
	child.workflowPending++
	c, store := child.command, u.proxy.orchestration.store
	generation := u.orchestrateGeneration
	u.orchestrateWork(func() func() {
		err := apply(u.orchestrateStorageContext, store)
		batches, readErr := store.Snapshot(c.workspace, c.main)
		var updated *orchestrate.Batch
		if readErr == nil {
			for _, b := range batches {
				if b.TaskName == child.batch.TaskName {
					updated = &b
					break
				}
			}
		}
		return func() {
			child.workflowPending--
			if u.orchestrateGeneration != generation {
				return
			}
			if updated != nil {
				child.batch = *updated
				if u.navigation != nil {
					for i, b := range u.navigation.retained {
						if b.TaskName == updated.TaskName {
							u.navigation.retained[i] = *updated
						}
					}
				}
			}
			next(errors.Join(err, readErr))
			u.orchestrationRoster()
			u.dirty = true
			if err == nil && readErr == nil {
				u.driveOrchestrateWorkflows()
			}
		}
	})
}

func (u *appServerUI) workflowFailure(child *orchestrateChild, phase orchestrate.WorkflowPhase, err error) {
	if !implementationWorkflow(child) {
		return
	}
	child.workflowEnding = true
	child.workflowStopped = true
	for _, work := range child.workflowWork {
		work.canceled = true
	}
	if !child.workflowDeferred {
		for _, work := range child.workflowWork {
			if work.completed != nil {
				continue
			}
			if work.dispatch.Kind == workflowValidation {
				_ = u.orchestrateRPCRequest(orchestrateRPC{method: "command/exec/terminate", child: child, review: work}, map[string]any{"processId": work.dispatch.ID})
			} else if work.dispatch.TurnID != "" {
				_ = u.orchestrateCleanupInterrupt(child, work.dispatch.ThreadID, work.dispatch.TurnID)
			}
		}
	}
	c := child.command
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		if workflowTerminal(child.batch.Workflow) {
			return nil
		}
		return s.StopWorkflow(ctx, c.workspace, c.main, child.batch.TaskName, phase, err.Error())
	}, func(saveErr error) {
		if saveErr != nil {
			u.orchestrateStorageErr = errors.Join(u.orchestrateStorageErr, saveErr)
			u.setNotice("Workflow state: "+saveErr.Error(), true)
			return
		}
		u.workflowOutcome(child)
	})
}

func (u *appServerUI) workflowOutcome(child *orchestrateChild) {
	if !implementationWorkflow(child) || child.workflowDeferred || !workflowTerminal(child.batch.Workflow) || child.batch.Workflow.Outcome != nil || child.workflowPending != 0 {
		return
	}
	c, w := child.command, child.batch.Workflow
	text := workflowOutcomeText(w)
	dispatch := false
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		var err error
		dispatch, err = s.BeginWorkflowOutcome(ctx, c.workspace, c.main, child.batch.TaskName, text)
		return err
	}, func(err error) {
		if err != nil {
			u.setNotice("Workflow outcome: "+err.Error(), true)
			return
		}
		if !dispatch {
			return
		}
		event := orchestrateEvent{Task: child.batch.TaskName, Kind: "done", Status: string(w.Phase), Message: text}
		if child.batch.Launch != nil {
			event.Thread = child.batch.Launch.ThreadID
		}
		if w.Phase != orchestrate.PhaseReady {
			event.Kind = "failure"
		}
		u.deliverOrchestratedAnswer(child, event, text)
	})
}

// workflowOutcomeText gives the coordinator what it needs to act without
// inspecting the run: the implementer report and, for a blocker, its evidence.
func workflowOutcomeText(w *orchestrate.Workflow) string {
	var text strings.Builder
	text.WriteString("Workflow " + string(w.Phase))
	if w.Candidate != nil {
		text.WriteString(" at " + w.Candidate.Revision)
	}
	if w.Reason != "" {
		text.WriteString(": " + w.Reason)
	}
	if w.ReviewWaiver != "" {
		text.WriteString("\nFurther reviews waived by coordinator at user request: " + w.ReviewWaiver)
	}
	if w.Phase != orchestrate.PhaseReady || w.ReviewWaiver != "" {
		writeWorkflowEvidence(&text, w)
	}
	if w.Report != "" {
		text.WriteString("\nImplementer report:\n" + w.Report)
	}
	switch w.Phase {
	case orchestrate.PhaseReady:
	case orchestrate.PhaseCanceled:
		text.WriteString("\nSend a follow-up to continue the workflow.")
	default:
		text.WriteString("\nSend a follow-up to continue the workflow, or interrupt to cancel it.")
	}
	return text.String()
}

// writeWorkflowEvidence lists failed checks with their output tails and every
// review finding, namespaced by review purpose.
func writeWorkflowEvidence(text *strings.Builder, w *orchestrate.Workflow) {
	for _, check := range w.Checks {
		if check.Outcome == orchestrate.CheckPassed {
			continue
		}
		text.WriteString("\nCheck " + check.CheckID + " (" + check.Command + ") " + string(check.Outcome))
		if check.ExitCode != nil {
			fmt.Fprintf(text, " with exit code %d", *check.ExitCode)
		}
		if check.Output != "" {
			text.WriteString(". Output tail:\n" + check.Output)
		}
	}
	for _, review := range w.Reviews {
		if w.ReviewWaiver != "" {
			text.WriteString("\nRetained " + string(review.Purpose) + " review at " + review.Revision + ": " + string(review.Verdict))
		}
		for _, finding := range review.Findings {
			text.WriteString("\n" + string(review.Purpose) + "/" + finding.ID + ": " + finding.Detail)
		}
	}
}

func (u *appServerUI) workflowOutcomeResponse(child *orchestrateChild, event orchestrateEvent, hostErr error) {
	c := child.command
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		return s.RecordWorkflowOutcome(ctx, c.workspace, c.main, child.batch.TaskName, hostErr == nil)
	}, func(err error) {
		if err = errors.Join(hostErr, err); err != nil {
			u.orchestratedAnswerFailure(child, event, err)
		} else {
			u.publishOrchestratedEvent(child, event)
		}
	})
}

// Successful implementation and correction turns admit only an actual clean
// committed checkout. Completion prose is never candidate/check evidence; it is
// retained as the report for the coordinator.
func (u *appServerUI) workflowImplementationCompleted(child *orchestrateChild, turn appServerTurn, report string) {
	if child.batch.Launch.TurnID == "" {
		child.workflowCompletion, child.workflowReport = &turn, report
		return
	}
	child.workflowCompletion, child.workflowReport = nil, ""
	if work := child.workflowWork[orchestrate.DispatchCorrection]; work != nil && (work.dispatch.TurnID == "" || work.dispatch.TurnID == turn.ID) {
		work.completed, work.message = &turn, report
		u.finishWorkflowWork(work)
		return
	}
	if workflowTerminal(child.batch.Workflow) {
		return
	}
	if child.batch.Workflow.Phase != orchestrate.PhaseImplementation {
		return
	}
	if turn.Status != "completed" {
		u.workflowFailure(child, orchestrate.PhaseFailed, fmt.Errorf("implementation turn %s", turn.Status))
		return
	}
	c, name := child.command, child.batch.TaskName
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		return admitWorkflowResult(ctx, s, c, name, report)
	}, func(err error) {
		if err != nil {
			u.workflowFailure(child, orchestrate.PhaseNeedsDecision, err)
		}
	})
}

// admitWorkflowResult routes an implementer result that is not a clean
// committed checkout back to correction instead of stopping the workflow.
func admitWorkflowResult(ctx context.Context, s *orchestrate.Store, c *orchestrateCommand, name, report string) error {
	candidate, err := s.ObserveCandidate(ctx, c.workspace, c.main, name)
	if err != nil {
		return s.RouteWorkflowCorrection(ctx, c.workspace, c.main, name, "result is not a clean committed checkout: "+err.Error(), report)
	}
	return s.AdmitCandidate(ctx, c.workspace, c.main, name, candidate, report)
}

// resumeWorkflow starts a new round after new implementer input reaches a
// stopped or ready workflow.
func (u *appServerUI) resumeWorkflow(child *orchestrateChild) {
	child.workflowStopped, child.workflowEnding = false, false
	c, name := child.command, child.batch.TaskName
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		return s.ResumeWorkflow(ctx, c.workspace, c.main, name)
	}, func(err error) {
		if err != nil {
			u.orchestrateStorageErr = errors.Join(u.orchestrateStorageErr, err)
			u.setNotice("Workflow state: "+err.Error(), true)
		}
	})
}

// Each batch progresses independently; host capacity remains the host's policy.
// Implementation turns stay host-owned and are not driven here.
func (u *appServerUI) driveOrchestrateWorkflows() {
	if u.orchestrateClosing {
		return
	}
	var children []*orchestrateChild
	for _, child := range u.orchestrateThreads {
		if !implementationWorkflow(child) || child.workflowDeferred || child.workflowPending != 0 || u.navigation != nil && u.navigation.resumes[child.batch.Launch.ThreadID] != nil {
			continue
		}
		children = append(children, child)
	}
	slices.SortFunc(children, func(a, b *orchestrateChild) int {
		if a.batch.TaskName < b.batch.TaskName {
			return -1
		}
		if a.batch.TaskName > b.batch.TaskName {
			return 1
		}
		return 0
	})
	for _, child := range children {
		w := child.batch.Workflow
		if workflowTerminal(w) {
			u.workflowOutcome(child)
			continue
		}
		if child.workflowStopped {
			continue
		}
		switch w.Phase {
		case orchestrate.PhaseValidation:
			if len(child.workflowWork) == 0 {
				u.startWorkflowCheck(child)
			}
		case orchestrate.PhaseQueuedReview, orchestrate.PhaseReview:
			// Begin one review per store round trip; both then run concurrently.
			for _, kind := range []orchestrate.WorkflowDispatchKind{orchestrate.DispatchCorrectness, orchestrate.DispatchSimplification} {
				if child.workflowWork[kind] == nil && !workflowDispatched(w, kind) {
					u.beginWorkflowWork(child, kind)
					break
				}
			}
		case orchestrate.PhaseCorrection:
			if len(child.workflowWork) == 0 {
				u.beginWorkflowWork(child, orchestrate.DispatchCorrection)
			}
		}
	}
}

func workflowDispatched(w *orchestrate.Workflow, kind orchestrate.WorkflowDispatchKind) bool {
	return slices.ContainsFunc(w.Dispatches, func(d orchestrate.WorkflowDispatch) bool {
		return d.Generation == w.Generation && d.Kind == kind
	})
}

func (u *appServerUI) beginWorkflowWork(child *orchestrateChild, kind orchestrate.WorkflowDispatchKind) {
	c, w := child.command, child.batch.Workflow
	if kind == orchestrate.DispatchCorrection && child.turn != "" {
		u.workflowFailure(child, orchestrate.PhaseNeedsDecision, errors.New("implementer has unexpected active work"))
		return
	}
	id := fmt.Sprintf("workflow/%s/%d/%s", child.batch.TaskName, w.Generation, kind)
	work := &orchestrateReviewWork{child: child, dispatch: orchestrate.WorkflowDispatch{Kind: kind}}
	u.setWorkflowWork(child, work)
	var dispatch bool
	var record orchestrate.WorkflowDispatch
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		var err error
		record, dispatch, err = s.BeginWorkflowDispatch(ctx, c.workspace, c.main, child.batch.TaskName, id, w.Generation, kind)
		return err
	}, func(err error) {
		work.dispatch = record
		if err != nil || !dispatch {
			delete(child.workflowWork, kind)
			if err == nil {
				err = errors.New("retained dispatch needs host reconciliation")
			}
			u.workflowFailure(child, orchestrate.PhaseNeedsDecision, err)
			return
		}
		if u.workflowWorkStopped(work) {
			u.failWorkflowDispatch(work, false, context.Canceled)
			return
		}
		if kind == orchestrate.DispatchCorrectness || kind == orchestrate.DispatchSimplification {
			if err := u.orchestrateRPCRequest(orchestrateRPC{method: "config/read", child: child, review: work}, map[string]any{"cwd": c.workspace, "includeLayers": true}); err != nil {
				u.failWorkflowDispatch(work, false, err)
			}
		} else {
			work.dispatch.ThreadID = child.batch.Launch.ThreadID
			u.startWorkflowTurn(work)
		}
	})
}

func (u *appServerUI) failWorkflowDispatch(work *orchestrateReviewWork, uncertain bool, err error) {
	child, c := work.child, work.child.command
	u.workflowReviewError(work.dispatch.ThreadID, err.Error())
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		if saveErr := s.FailWorkflowDispatch(ctx, c.workspace, c.main, child.batch.TaskName, work.dispatch.ID, uncertain, err.Error()); saveErr != nil {
			return saveErr
		}
		if !uncertain && work.dispatch.ThreadID == "" {
			return s.ReleaseWorkflowReviewStorage(ctx, c.workspace, c.main, child.batch.TaskName, work.dispatch.ID)
		}
		return nil
	}, func(saveErr error) {
		if work.dispatch.TurnID == "" {
			u.endWorkflowWork(work)
		}
		if work.canceled {
			if saveErr != nil {
				u.setNotice("Workflow state: "+saveErr.Error(), true)
			}
			return
		}
		u.workflowFailure(child, orchestrate.PhaseNeedsDecision, errors.Join(err, saveErr))
	})
}

func (u *appServerUI) setWorkflowWork(child *orchestrateChild, work *orchestrateReviewWork) {
	if child.workflowWork == nil {
		child.workflowWork = map[orchestrate.WorkflowDispatchKind]*orchestrateReviewWork{}
	}
	child.workflowWork[work.dispatch.Kind] = work
}
func (u *appServerUI) endWorkflowWork(work *orchestrateReviewWork) {
	if work.child.lifecycleRecovery == work {
		work.child.lifecycleRecovery = nil
	}
	if work.child.workflowWork[work.dispatch.Kind] == work {
		delete(work.child.workflowWork, work.dispatch.Kind)
	}
}

func (u *appServerUI) finishWorkflowWork(work *orchestrateReviewWork) {
	if !work.acknowledged || work.completed == nil || work.processing {
		return
	}
	work.processing = true
	child, c, name, turn, d := work.child, work.child.command, work.child.batch.TaskName, *work.completed, work.dispatch
	message, item, messageTurn := work.message, work.item, work.messageTurn
	stopped := u.workflowWorkStopped(work)
	completionRetained := false
	if turn.ID != d.TurnID {
		u.workflowFailure(child, orchestrate.PhaseNeedsDecision, errors.New("workflow host turn identity changed"))
		return
	}
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		if err := s.RecordWorkflowCompletion(ctx, c.workspace, c.main, name, d.ID, d.ThreadID, turn.ID, turn.Status); err != nil {
			return err
		}
		completionRetained = true
		if stopped {
			return nil
		}
		if turn.Status != "completed" {
			return fmt.Errorf("workflow %s turn %s", d.Kind, turn.Status)
		}
		if d.Kind == orchestrate.DispatchCorrection {
			return admitWorkflowResult(ctx, s, c, name, message)
		}
		candidate, err := s.ObserveCandidate(ctx, c.workspace, c.main, name)
		if err != nil {
			return err
		}
		if candidate.Revision != d.Revision {
			return errors.New("candidate changed during review")
		}
		if messageTurn != turn.ID || item == "" {
			return errors.New("review has no completed result item")
		}
		result, err := orchestrate.DecodeReviewResult([]byte(message))
		if err != nil {
			return err
		}
		if orchestrate.WorkflowDispatchKind(result.Purpose) != d.Kind {
			return errors.New("review returned the wrong purpose")
		}
		return s.RecordWorkflowReview(ctx, c.workspace, c.main, name, d.ID, orchestrate.HostReceipt{ThreadID: d.ThreadID, TurnID: turn.ID, ItemID: item}, result)
	}, func(err error) {
		u.endWorkflowWork(work)
		if err != nil && !work.canceled {
			phase := orchestrate.PhaseNeedsDecision
			if turn.Status != "completed" {
				phase = orchestrate.PhaseFailed
			}
			u.workflowFailure(child, phase, err)
		}
		if completionRetained {
			u.archiveWorkflowReviewer(work)
		}
	})
}

func (u *appServerUI) interruptWorkflow(child *orchestrateChild, c *orchestrateCommand) {
	child.workflowEnding = true
	child.workflowDeferred = false
	if child.turn != "" && child.workflowWork[orchestrate.DispatchCorrection] == nil {
		_ = u.orchestrateCleanupInterrupt(child, child.batch.Launch.ThreadID, child.turn)
	}
	u.workflowFailure(child, orchestrate.PhaseCanceled, errors.New("workflow interrupted"))
	u.orchestrateWork(func() func() { return func() { c.reply <- orchestrateResult{batch: child.batch} } })
}
