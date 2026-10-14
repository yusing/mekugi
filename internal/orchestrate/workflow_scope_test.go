package orchestrate

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowScopeAmendmentDelivery(t *testing.T) {
	for _, state := range []string{"delivered", "canceled", "rejected", "uncertain", "dispatching"} {
		t.Run(state, func(t *testing.T) {
			f := newWorkflowFixture(t, []RequiredCheck{{"check", "focused check"}})
			for generation := 1; generation <= 2; generation++ {
				f.check(CheckResult{Generation: generation, Phase: PhaseValidation, Command: "focused check", Cwd: f.batch.Cwd, Revision: f.batch.Base, CheckID: "check", Outcome: CheckPassed, ExitCode: new(0)})
				verdict := VerdictClear
				if generation == 1 {
					verdict = VerdictFindings
				}
				f.review(DispatchCorrectness, verdict)
				f.review(DispatchSimplification, VerdictClear)
				for _, d := range f.state().Dispatches {
					if d.HostStatus == "" {
						f.ok(f.s.RecordWorkflowCompletion(t.Context(), f.workspace, "main", "batch", d.ID, d.ThreadID, d.TurnID, "completed"))
					}
				}
				if generation == 1 {
					f.dispatch(DispatchCorrection)
					f.ok(f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{f.batch.Base}, "dispute finding"))
				}
			}
			f.ok(f.s.ObserveLaunch(t.Context(), f.workspace, "main", "batch", "completed", ""))
			_, err := f.s.RecordIntegration(t.Context(), f.workspace, "main", "batch", "implementer")
			f.ok(err)
			before := f.state()
			if len(before.PreviousReviews) == 0 || len(before.Checks) == 0 {
				t.Fatal("fixture needs prior delta coverage and validation")
			}
			input := Delivery{ID: "amend", From: "main", Target: "implementer", Message: "continue", ReviewScope: "Attribution only; exclude transport redesign"}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, _, _, err := f.s.BeginTurnDelivery(ctx, f.workspace, "main", input); err == nil || !reflect.DeepEqual(before, f.state()) {
				t.Fatal("cancellation before persistence changed scope")
			}
			for _, bad := range []Delivery{
				{ID: "bad", From: "implementer", Target: "main", Message: "report", ReviewScope: input.ReviewScope},
				{ID: "bad", From: "main", Target: "implementer", Message: "steer", ReviewScope: input.ReviewScope, TurnID: "running"},
				{ID: "bad", From: "main", Target: "implementer", Message: "queue", ReviewScope: input.ReviewScope, Deferred: true},
				{ID: "bad", From: "main", Target: "implementer", Message: "blank", ReviewScope: " "},
			} {
				if _, _, _, err := f.s.BeginTurnDelivery(t.Context(), f.workspace, "main", bad); err == nil {
					t.Fatal("unauthorized amendment accepted", bad)
				}
			}
			_, text, dispatch, err := f.s.BeginTurnDelivery(t.Context(), f.workspace, "main", input)
			f.ok(err)
			if !dispatch || !strings.Contains(text, input.ReviewScope) {
				t.Fatal("implementer did not receive explicit scope", text)
			}
			turn := ""
			if state == "delivered" {
				turn = "followup"
			}
			if state != "dispatching" {
				_, err = f.s.RecordDelivery(t.Context(), f.workspace, "main", input.ID, state, turn, "")
				f.ok(err)
			}
			f.s = &Store{Directory: f.s.Directory}
			batches, err := f.s.Snapshot(f.workspace, "main")
			f.ok(err)
			if batches[0].Integration != nil {
				t.Fatal("amendment retained prior integration approval")
			}
			d, _, dispatch, err := f.s.BeginTurnDelivery(t.Context(), f.workspace, "main", input)
			f.ok(err)
			if dispatch || d.State != state || d.TurnID != turn || d.ReviewScope != input.ReviewScope {
				t.Fatal("replay changed delivery facts", d)
			}
			if state == "dispatching" || state == "uncertain" {
				next := input
				next.ID, next.ReviewScope = "next-amendment", "different scope"
				if _, _, _, err := f.s.BeginTurnDelivery(t.Context(), f.workspace, "main", next); err == nil {
					t.Fatal("unresolved delivery authorized another amendment")
				}
			}
			w := f.state()
			if w.Assignment.Scope != input.ReviewScope || w.Assignment.Kind != before.Assignment.Kind || !reflect.DeepEqual(w.Assignment.RequiredChecks, before.Assignment.RequiredChecks) || w.ReviewWaiver != before.ReviewWaiver || len(w.Reviews)+len(w.PreviousReviews)+len(w.Checks) != 0 || w.Outcome != nil || workflowReady(Batch{Workflow: w}, f.batch.Base) == nil {
				t.Fatal("amendment lost authority or retained approvals", w)
			}
			input.ReviewScope = "conflicting repeat"
			if _, _, _, err := f.s.BeginTurnDelivery(t.Context(), f.workspace, "main", input); err == nil {
				t.Fatal("same-call scope conflict accepted")
			}
			f.ok(f.s.ResumeWorkflow(t.Context(), f.workspace, "main", "batch"))
			f.ok(f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{f.batch.Base}, "report claims a different scope"))
			if w = f.state(); w.Assignment.Scope != d.ReviewScope || len(w.PreviousReviews) != 0 || len(w.Reviews) != 0 {
				t.Fatal("unchanged candidate restored stale review scope", w)
			}
			if state == "uncertain" {
				// Old starts, unidentified completions and a different turn's completion
				// cannot certify settlement of the retained uncertain effect.
				for _, observation := range [][2]string{{"implementation", "running"}, {"implementation", "completed"}, {"unknown", "completed"}, {"observed", "running"}, {"other", "completed"}} {
					f.ok(f.s.ObserveDeliveryTurn(t.Context(), f.workspace, "main", "batch", observation[0], observation[1]))
					got, _, err := f.s.BeginDelivery(t.Context(), f.workspace, "main", d)
					f.ok(err)
					if !got.unsettled() {
						t.Fatal("unrelated or active host work settled uncertainty", observation, got)
					}
				}
				f.s = &Store{Directory: f.s.Directory}
				f.ok(f.s.ObserveDeliveryTurn(t.Context(), f.workspace, "main", "batch", "observed", "completed"))
				got, _, err := f.s.BeginDelivery(t.Context(), f.workspace, "main", d)
				f.ok(err)
				if got.unsettled() || got.State != "uncertain" || got.TurnID != "" || got.ObservedTurnID != "observed" {
					t.Fatal("matching host completion lost settlement or delivery provenance", got)
				}
			}
		})
	}
}
