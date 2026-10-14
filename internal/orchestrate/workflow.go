package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Workflow retains facts only. The runtime owns permissions, validation command
// execution, host lifecycle reconciliation, capacity, and user notifications.
type Workflow struct {
	ReviewWaiver     string             `json:"review_waiver,omitempty"`
	Outcome          *WorkflowOutcome   `json:"outcome,omitempty"`
	Assignment       WorkflowAssignment `json:"assignment"`
	Phase            WorkflowPhase      `json:"phase"`
	Candidate        *Candidate         `json:"candidate,omitempty"`
	Generation       int                `json:"generation"`
	Checks           []CheckResult      `json:"checks,omitempty"`
	Reviews          []ReviewResult     `json:"reviews,omitempty"`
	PreviousReviews  []PreviousReview   `json:"previous_reviews,omitempty"`
	CorrectionRounds int                `json:"correction_rounds"`
	Dispatches       []WorkflowDispatch `json:"dispatches,omitempty"`
	Reason           string             `json:"reason,omitempty"`
	Report           string             `json:"report,omitempty"`
}

func (s *Store) ConfigureWorkflow(ctx context.Context, workspace, main, name string, assignment WorkflowAssignment) error {
	if strings.TrimSpace(assignment.Scope) == "" || assignment.Kind != WorkflowImplementation && assignment.Kind != WorkflowDiagnosis {
		return errors.New("workflow requires a scope and assignment kind")
	}
	if assignment.Kind == WorkflowDiagnosis && len(assignment.RequiredChecks) != 0 {
		return errors.New("diagnosis cannot declare implementation checks")
	}
	seen := map[string]bool{}
	for _, check := range assignment.RequiredChecks {
		if strings.TrimSpace(check.ID) == "" || strings.TrimSpace(check.Command) == "" || seen[check.ID] {
			return errors.New("required checks need unique identities and commands")
		}
		seen[check.ID] = true
	}
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		if b.Workflow != nil {
			scope := b.Workflow.Assignment.Scope
			if b.Launch != nil {
				scope = b.Launch.Message // Replay validates the original request, not amended review authority.
			}
			if b.Workflow.Assignment.Kind == assignment.Kind && scope == assignment.Scope && slices.Equal(b.Workflow.Assignment.RequiredChecks, assignment.RequiredChecks) {
				return errLaunchUnchanged
			}
			return errors.New("workflow assignment changed")
		}
		if b.State != "prepared" || b.Launch != nil {
			return errors.New("configure workflow before launch")
		}
		b.Workflow = &Workflow{Assignment: assignment, Phase: PhaseImplementation}
		return nil
	})
}
func workflowFor(b *Batch) (*Workflow, error) {
	if b.State == "removing" || b.State == "removed" || b.Workflow == nil || b.Workflow.Assignment.Kind != WorkflowImplementation {
		return nil, errors.New("batch has no active implementation workflow")
	}
	return b.Workflow, nil
}
func (w *Workflow) stopped() bool {
	return w.Phase == PhaseNeedsDecision || w.Phase == PhaseCanceled || w.Phase == PhaseFailed
}

// AdmitCandidate accepts only the runtime's clean, host-observed revision after
// an implementation or correction turn. A correction may dispute findings without
// changing revision; it still starts a fresh validation/review generation. No
// previous approvals survive replacement. Waived reviews remain historical evidence.
// The report is the implementer's final
// message, retained only for the coordinator notification.
func (s *Store) AdmitCandidate(ctx context.Context, workspace, main, name string, candidate Candidate, report string) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		w, err := workflowFor(b)
		if err != nil {
			return err
		}
		if w.Phase != PhaseImplementation && w.Phase != PhaseCorrection {
			if w.Candidate != nil && *w.Candidate == candidate {
				return errLaunchUnchanged
			}
			return errors.New("workflow is not awaiting a candidate")
		}
		if w.Phase == PhaseCorrection && !w.correctionTurnAcknowledged() {
			return errors.New("correction candidate requires an acknowledged correction turn")
		}
		w.retainPreviousReviews()
		w.Outcome = nil
		w.Candidate = &candidate
		w.Generation++
		w.Checks = nil
		for _, check := range w.Assignment.RequiredChecks {
			w.Dispatches = append(w.Dispatches, WorkflowDispatch{
				ID:   fmt.Sprintf("workflow/%s/%d/check/%s", name, w.Generation, check.ID),
				Kind: DispatchValidation, CheckID: check.ID, Generation: w.Generation,
				Revision: candidate.Revision, State: DispatchQueued,
			})
		}
		if w.ReviewWaiver == "" {
			w.Reviews = nil
		}
		w.Reason = ""
		w.Report = report
		w.Phase = PhaseValidation
		if len(w.Assignment.RequiredChecks) == 0 {
			w.Phase = PhaseQueuedReview
			if w.ReviewWaiver != "" {
				w.Phase = PhaseReady
			}
		}
		return nil
	})
}

func (w *Workflow) correctionTurnAcknowledged() bool {
	d := w.currentDispatch(DispatchCorrection)
	return d != nil && d.State == DispatchAcknowledged
}

// Keep the last reviewed candidate across failed checks and unclean corrections.
// Dispatch identities retain host provenance; these results are never approvals
// for the replacement candidate. ResumeWorkflow starts a new scope instead.
func (w *Workflow) retainPreviousReviews() {
	if len(w.Reviews) == 0 || w.ReviewWaiver != "" {
		return
	}
	w.PreviousReviews = nil
	for _, review := range w.Reviews {
		if d := w.currentDispatch(WorkflowDispatchKind(review.Purpose)); d != nil && d.ResultItemID != "" {
			w.PreviousReviews = append(w.PreviousReviews, PreviousReview{DispatchID: d.ID, Result: review})
		}
	}
}

// routeCorrection sends a rejected generation back to the implementer until the
// round budget is spent; then the coordinator decides.
func (w *Workflow) routeCorrection(reason string) {
	w.Reason = reason
	w.Phase = PhaseCorrection
	if w.CorrectionRounds >= 2 {
		w.Phase = PhaseNeedsDecision
		w.Reason = reason + " after two correction rounds"
	}
}

// RouteWorkflowCorrection rejects an implementer result that cannot become a
// candidate, such as an uncommitted or unclean checkout. It opens a fresh
// generation so the correction dispatch is not confused with an earlier one.
func (s *Store) RouteWorkflowCorrection(ctx context.Context, workspace, main, name, reason, report string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("correction route requires a reason")
	}
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		w, err := workflowFor(b)
		if err != nil {
			return err
		}
		if w.Phase != PhaseImplementation && (w.Phase != PhaseCorrection || !w.correctionTurnAcknowledged()) {
			return errors.New("workflow is not awaiting a candidate")
		}
		w.retainPreviousReviews()
		w.Outcome = nil
		w.Generation++
		w.Checks = nil
		if w.ReviewWaiver == "" {
			w.Reviews = nil
		}
		w.Report = report
		w.routeCorrection(reason)
		return nil
	})
}

// ResumeWorkflow reopens implementation after the coordinator or user sends the
// implementer new input. The new round has a fresh correction budget.
func (s *Store) ResumeWorkflow(ctx context.Context, workspace, main, name string) error {
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		w, err := workflowFor(b)
		if err != nil {
			return err
		}
		if !w.stopped() && w.Phase != PhaseReady {
			return errLaunchUnchanged
		}
		w.Phase = PhaseImplementation
		w.Generation++
		w.CorrectionRounds = 0
		w.PreviousReviews = nil
		w.Checks = nil
		if w.ReviewWaiver == "" {
			w.Reviews = nil
		}
		w.Reason = ""
		w.Report = ""
		w.Outcome = nil
		return nil
	})
}

func (s *Store) RecordWorkflowCheck(ctx context.Context, workspace, main, name string, result CheckResult) error {
	if err := result.Validate(); err != nil {
		return err
	}
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		w, err := workflowFor(b)
		if err != nil {
			return err
		}
		if w.Candidate == nil || result.Revision != w.Candidate.Revision || result.Generation != w.Generation {
			return errors.New("check is for a stale candidate")
		}
		i := slices.IndexFunc(w.Assignment.RequiredChecks, func(c RequiredCheck) bool { return c.ID == result.CheckID })
		if i < 0 || w.Assignment.RequiredChecks[i].Command != result.Command || result.Cwd != b.Cwd {
			return errors.New("check does not match its declared command or checkout")
		}
		for _, old := range w.Checks {
			if old.CheckID == result.CheckID {
				if reflect.DeepEqual(old, result) {
					return errLaunchUnchanged
				}
				return errors.New("check receipt changed")
			}
		}
		if w.Phase != PhaseValidation {
			return errors.New("workflow is not awaiting validation")
		}
		d := slices.IndexFunc(w.Dispatches, func(d WorkflowDispatch) bool {
			return d.Kind == DispatchValidation && d.Generation == result.Generation && d.CheckID == result.CheckID && d.Revision == result.Revision
		})
		if d < 0 || w.Dispatches[d].State != DispatchIntent && w.Dispatches[d].State != DispatchUncertain {
			return errors.New("check has no retained dispatch intent")
		}
		w.Dispatches[d].State = DispatchAcknowledged
		w.Dispatches[d].HostStatus = "completed"
		if result.Outcome == CheckUncertain {
			w.Dispatches[d].State = DispatchUncertain
			w.Dispatches[d].HostStatus = ""
		}
		w.Checks = append(w.Checks, result)
		switch result.Outcome {
		case CheckFailed:
			w.routeCorrection("required check " + result.CheckID + " failed")
			return nil
		case CheckUncertain:
			w.Phase = PhaseNeedsDecision
			w.Reason = "required check " + result.CheckID + " has no confirmed exit status"
			return nil
		}
		if len(w.Checks) == len(w.Assignment.RequiredChecks) {
			w.Phase = PhaseQueuedReview
			if w.ReviewWaiver != "" {
				w.Phase = PhaseReady
			}
		}
		return nil
	})
}

// RecordWorkflowReview binds schema-validated output to the acknowledged review
// turn. Runtime must observe successful host turn completion before this call.
func (s *Store) RecordWorkflowReview(ctx context.Context, workspace, main, name, id string, host HostReceipt, result ReviewResult) error {
	if err := result.Validate(); err != nil {
		return err
	}
	if !host.valid() {
		return errors.New("review requires a host result receipt")
	}
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		w, err := workflowFor(b)
		if err != nil {
			return err
		}
		d := w.currentDispatch(WorkflowDispatchKind(result.Purpose))
		if d == nil || d.ID != id || d.State != DispatchAcknowledged || d.ThreadID != host.ThreadID || d.TurnID != host.TurnID || w.Candidate == nil || result.Revision != w.Candidate.Revision {
			return errors.New("review does not match an acknowledged candidate dispatch")
		}
		for _, old := range w.Reviews {
			if old.Purpose == result.Purpose {
				if reflect.DeepEqual(old, result) && d.ResultItemID == host.ItemID {
					return errLaunchUnchanged
				}
				return errors.New("review result changed")
			}
		}
		if w.stopped() || w.Phase != PhaseReview {
			return errors.New("workflow is not awaiting reviews")
		}
		d.ResultItemID = host.ItemID
		w.Reviews = append(w.Reviews, result)
		if len(w.Reviews) != 2 {
			return nil
		}
		for _, r := range w.Reviews {
			if r.Verdict == VerdictBlocked {
				w.Phase = PhaseNeedsDecision
				w.Reason = string(r.Purpose) + ": " + r.Reason
				return nil
			}
		}
		for _, r := range w.Reviews {
			if r.Verdict == VerdictFindings {
				w.routeCorrection("reviews reported findings")
				return nil
			}
		}
		w.Phase = PhaseReady
		return nil
	})
}

// BlockWorkflowCandidate retains an implementation result requiring a decision,
// without admitting a candidate or spending an automatic correction round.
func (s *Store) BlockWorkflowCandidate(ctx context.Context, workspace, main, name, reason, report string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("candidate blocker requires a reason")
	}
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		w, err := workflowFor(b)
		if err != nil {
			return err
		}
		if w.Phase != PhaseImplementation && (w.Phase != PhaseCorrection || !w.correctionTurnAcknowledged()) {
			return errors.New("workflow is not awaiting a candidate")
		}
		w.Phase, w.Reason, w.Report = PhaseNeedsDecision, reason, report
		return nil
	})
}

func (s *Store) StopWorkflow(ctx context.Context, workspace, main, name string, phase WorkflowPhase, reason string) error {
	if phase != PhaseNeedsDecision && phase != PhaseCanceled && phase != PhaseFailed || strings.TrimSpace(reason) == "" {
		return errors.New("workflow stop requires a terminal phase and reason")
	}
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		w, err := workflowFor(b)
		if err != nil {
			return err
		}
		if w.Phase == phase && w.Reason == reason {
			return errLaunchUnchanged
		}
		if w.stopped() {
			return errors.New("workflow already stopped")
		}
		w.Phase, w.Reason = phase, reason
		return nil
	})
}

func workflowReady(b Batch, revision string) error {
	w := b.Workflow
	if w == nil {
		return nil
	} // Retained legacy batches retain their original contract.
	if w.Assignment.Kind != WorkflowImplementation || w.Phase != PhaseReady || w.Candidate == nil || w.Candidate.Revision != revision {
		return errors.New("integration requires workflow readiness for the exact candidate revision")
	}
	if len(w.Checks) != len(w.Assignment.RequiredChecks) {
		return errors.New("workflow approvals are incomplete")
	}
	for _, check := range w.Assignment.RequiredChecks {
		i := slices.IndexFunc(w.Checks, func(r CheckResult) bool { return r.CheckID == check.ID })
		if i < 0 {
			return errors.New("required check is missing")
		}
		r := w.Checks[i]
		if r.Validate() != nil || r.Revision != revision || r.Generation != w.Generation || r.Command != check.Command || r.Cwd != b.Cwd || r.Outcome != CheckPassed {
			return fmt.Errorf("required check %s is not approved", check.ID)
		}
	}
	if w.ReviewWaiver != "" {
		return nil
	}
	if len(w.Reviews) != 2 {
		return errors.New("workflow review approvals are incomplete")
	}
	for _, purpose := range []ReviewPurpose{ReviewCorrectness, ReviewSimplification} {
		i := slices.IndexFunc(w.Reviews, func(r ReviewResult) bool { return r.Purpose == purpose })
		d := w.currentDispatch(WorkflowDispatchKind(purpose))
		if i < 0 || d == nil || d.State != DispatchAcknowledged || d.ResultItemID == "" {
			return errors.New("review approval is missing")
		}
		r := w.Reviews[i]
		if r.Validate() != nil || r.Revision != revision || r.Verdict != VerdictClear {
			return errors.New("review does not approve this candidate")
		}
	}
	return nil
}
