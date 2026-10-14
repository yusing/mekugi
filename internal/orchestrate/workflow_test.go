package orchestrate

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

type workflowFixture struct {
	t         *testing.T
	s         *Store
	workspace string
	batch     Batch
}

func newWorkflowFixture(t *testing.T, checks []RequiredCheck) *workflowFixture {
	t.Helper()
	s, workspace, b := launchFixture(t)
	f := &workflowFixture{t, s, workspace, b}
	f.ok(s.ConfigureWorkflow(t.Context(), workspace, "main", "batch", WorkflowAssignment{Kind: WorkflowImplementation, Scope: "bounded assignment", RequiredChecks: checks}))
	f.ok(s.ConfigureWorkflow(t.Context(), workspace, "main", "batch", WorkflowAssignment{Kind: WorkflowImplementation, Scope: "bounded assignment", RequiredChecks: checks}))
	_, _, err := s.BeginLaunch(t.Context(), workspace, "main", "batch", "implement", jsontext.Value(`{}`), nil)
	f.ok(err)
	f.ok(s.RecordThread(t.Context(), workspace, "main", "batch", "implementer"))
	f.ok(s.RecordTurn(t.Context(), workspace, "main", "batch", "implementer", "implementation"))
	f.ok(s.AdmitCandidate(t.Context(), workspace, "main", "batch", Candidate{b.Base}, "report"))
	return f
}
func (f *workflowFixture) ok(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}
func (f *workflowFixture) state() *Workflow {
	f.t.Helper()
	b, err := (&Store{Directory: f.s.Directory}).Snapshot(f.workspace, "main")
	f.ok(err)
	return b[0].Workflow
}
func (f *workflowFixture) dispatch(kind WorkflowDispatchKind) WorkflowDispatch {
	f.t.Helper()
	w := f.state()
	id := fmt.Sprintf("%d-%s", w.Generation, kind)
	d, launch, err := f.s.BeginWorkflowDispatch(f.t.Context(), f.workspace, "main", "batch", id, w.Generation, kind)
	f.ok(err)
	if !launch {
		f.t.Fatal("new dispatch not granted")
	}
	thread := d.ThreadID
	if thread == "" {
		thread = id
	}
	if kind == DispatchCorrection {
		thread = "implementer"
	}
	f.ok(f.s.RecordWorkflowDispatch(f.t.Context(), f.workspace, "main", "batch", id, thread, ""))
	f.ok(f.s.RecordWorkflowDispatch(f.t.Context(), f.workspace, "main", "batch", id, thread, id+"-turn"))
	d.ThreadID, d.TurnID = thread, id+"-turn"
	return d
}
func (f *workflowFixture) check(result CheckResult) {
	f.t.Helper()
	id := fmt.Sprintf("workflow/batch/%d/check/%s", result.Generation, result.CheckID)
	_, _, err := f.s.BeginWorkflowDispatch(f.t.Context(), f.workspace, "main", "batch", id, result.Generation, DispatchValidation)
	f.ok(err)
	f.ok(f.s.RecordWorkflowCheck(f.t.Context(), f.workspace, "main", "batch", result))
}
func (f *workflowFixture) review(kind WorkflowDispatchKind, verdict ReviewVerdict) {
	f.t.Helper()
	d := f.dispatch(kind)
	r := ReviewResult{Revision: d.Revision, Purpose: ReviewPurpose(kind), Verdict: verdict, Findings: []ReviewFinding{}}
	if verdict == VerdictFindings {
		r.Findings = []ReviewFinding{{"F1", "fix or dispute this finding"}}
	}
	if verdict == VerdictBlocked {
		r.Reason = "missing host capability"
	}
	f.ok(f.s.RecordWorkflowReview(f.t.Context(), f.workspace, "main", "batch", d.ID, HostReceipt{d.ThreadID, d.TurnID, "result"}, r))
}

func TestWorkflowChecksReviewsAndIntegration(t *testing.T) {
	f := newWorkflowFixture(t, []RequiredCheck{{"offline", "never execute this string"}})
	if _, _, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "early", 1, DispatchCorrectness); err == nil {
		t.Fatal("review skipped required checks")
	}
	zero := 0
	r := CheckResult{Generation: 1, Phase: PhaseValidation, Command: "never execute this string", Cwd: f.batch.Cwd, Revision: f.batch.Base, CheckID: "offline", Outcome: CheckPassed, ExitCode: &zero}
	wrong := r
	if err := f.s.RecordWorkflowCheck(t.Context(), f.workspace, "main", "batch", r); err == nil {
		t.Fatal("unsent check supplied evidence without dispatch intent")
	}
	wrong.Command = "another command"
	if err := f.s.RecordWorkflowCheck(t.Context(), f.workspace, "main", "batch", wrong); err == nil {
		t.Fatal("undeclared command supplied check evidence")
	}
	f.check(r)
	f.check(r)
	if f.state().Phase != PhaseQueuedReview {
		t.Fatal("checks did not queue reviews")
	}
	f.review(DispatchCorrectness, VerdictClear)
	if _, err := f.s.RecordIntegration(t.Context(), f.workspace, "main", "batch", "implementer"); err == nil {
		t.Fatal("one approval integrated")
	}
	f.review(DispatchSimplification, VerdictClear)
	proof, err := f.s.RecordIntegration(t.Context(), f.workspace, "main", "batch", "implementer")
	f.ok(err)
	if proof.Tip != f.batch.Base {
		t.Fatal("wrong integration proof")
	}
	// A new actual checkout commit must be reviewed, even though the stored phase is ready.
	_, err = git(t.Context(), f.batch.Cwd, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "new candidate")
	f.ok(err)
	tip, err := sourceBase(t.Context(), f.batch.Cwd)
	f.ok(err)
	if _, err := f.s.RecordIntegration(t.Context(), f.workspace, "main", "batch", "implementer"); err == nil {
		t.Fatal("stale candidate integrated")
	}
	if err := f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{tip}, "report"); err == nil {
		t.Fatal("ready workflow admitted a candidate without new implementer input")
	}
	f.ok(f.s.ResumeWorkflow(t.Context(), f.workspace, "main", "batch"))
	f.ok(f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{tip}, "report"))
	w := f.state()
	if w.Phase != PhaseValidation || len(w.Checks) != 0 || len(w.Reviews) != 0 || w.Generation != 3 {
		t.Fatal("revision did not invalidate approvals", w)
	}
	if err := f.s.RecordWorkflowCheck(t.Context(), f.workspace, "main", "batch", r); err == nil {
		t.Fatal("stale check accepted")
	}
}

func TestWorkflowCorrectionLimitAndAggregation(t *testing.T) {
	f := newWorkflowFixture(t, nil)
	for round := 0; round < 3; round++ {
		f.review(DispatchCorrectness, VerdictFindings)
		if f.state().Phase != PhaseReview {
			t.Fatal("partial review delivered findings")
		}
		f.review(DispatchSimplification, VerdictClear)
		w := f.state()
		if round == 2 {
			if w.Phase != PhaseNeedsDecision || w.CorrectionRounds != 2 {
				t.Fatal("correction limit not enforced", w)
			}
			break
		}
		if w.Phase != PhaseCorrection || len(w.Reviews) != 2 {
			t.Fatal("both reviews not aggregated", w)
		}
		for _, d := range w.Dispatches {
			if d.Generation == w.Generation {
				f.ok(f.s.RecordWorkflowCompletion(t.Context(), f.workspace, "main", "batch", d.ID, d.ThreadID, d.TurnID, "completed"))
			}
		}
		f.dispatch(DispatchCorrection)
		// A dispute may keep the commit but must invalidate both previous approvals.
		f.ok(f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{f.batch.Base}, "report"))
		next := f.state() // Reload from disk, including exact prior verdict provenance.
		if len(next.Reviews) != 0 || len(next.PreviousReviews) != 2 {
			t.Fatal("dispute reused approvals or lost prior context", next)
		}
		for i, previous := range next.PreviousReviews {
			d := w.currentDispatch(WorkflowDispatchKind(previous.Result.Purpose))
			if !reflect.DeepEqual(previous.Result, w.Reviews[i]) || previous.DispatchID != d.ID || d.ResultItemID != "result" {
				t.Fatal("prior verdict lost its candidate, purpose, findings, or host provenance", previous)
			}
		}
	}
	if _, _, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "extra", 3, DispatchCorrection); err == nil {
		t.Fatal("third correction dispatched")
	}
	f.ok(f.s.ResumeWorkflow(t.Context(), f.workspace, "main", "batch"))
	f.ok(f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{f.batch.Base}, "new scope"))
	if len(f.state().PreviousReviews) != 0 {
		t.Fatal("new implementation scope inherited delta review context")
	}
}

func TestWorkflowPriorReviewsSurviveRejectedCorrections(t *testing.T) {
	f := newWorkflowFixture(t, []RequiredCheck{{"check", "command"}})
	r := CheckResult{Generation: 1, Phase: PhaseValidation, Command: "command", Cwd: f.batch.Cwd, Revision: f.batch.Base, CheckID: "check", Outcome: CheckPassed, ExitCode: new(0)}
	f.check(r)
	f.review(DispatchCorrectness, VerdictFindings)
	f.review(DispatchSimplification, VerdictClear)
	f.dispatch(DispatchCorrection)
	f.ok(f.s.RouteWorkflowCorrection(t.Context(), f.workspace, "main", "batch", "unclean correction", "disputed F1"))
	previous := f.state().PreviousReviews
	if len(previous) != 2 || previous[0].Result.Findings[0].ID != "F1" {
		t.Fatal("unclean correction lost the original findings", previous)
	}
	f.dispatch(DispatchCorrection)
	f.ok(f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{f.batch.Base}, "committed"))
	r.Generation, r.Outcome, r.ExitCode = f.state().Generation, CheckFailed, new(1)
	f.check(r)
	if w := f.state(); !reflect.DeepEqual(w.PreviousReviews, previous) || len(w.Reviews) != 0 || w.Phase != PhaseNeedsDecision {
		t.Fatal("failed validation lost review context or reused approvals", w)
	}
}

func TestWorkflowReviewWaiverRetainsEvidenceAndValidation(t *testing.T) {
	f := newWorkflowFixture(t, []RequiredCheck{{"check", "focused check"}})
	zero := 0
	check := CheckResult{Generation: 1, Phase: PhaseValidation, Command: "focused check", Cwd: f.batch.Cwd, Revision: f.batch.Base, CheckID: "check", Outcome: CheckPassed, ExitCode: &zero}
	f.check(check)
	f.review(DispatchCorrectness, VerdictFindings)
	f.review(DispatchSimplification, VerdictClear)
	input := Delivery{ID: "waiver", From: "main", Target: "implementer", Message: "finish corrections", ReviewWaiver: "User requested no more reviews"}
	if _, _, _, err := f.s.BeginTurnDelivery(t.Context(), f.workspace, "main", input); err == nil {
		t.Fatal("active workflow accepted a waiver")
	}
	f.ok(f.s.StopWorkflow(t.Context(), f.workspace, "main", "batch", PhaseCanceled, "user stopped"))
	f.ok(f.s.ObserveLaunch(t.Context(), f.workspace, "main", "batch", "completed", ""))
	if _, _, _, err := f.s.BeginTurnDelivery(t.Context(), f.workspace, "main", input); err == nil {
		t.Fatal("unsettled reviewer accepted a waiver")
	}
	for _, d := range f.state().Dispatches {
		if d.Kind == DispatchValidation {
			continue
		}
		f.ok(f.s.RecordWorkflowCompletion(t.Context(), f.workspace, "main", "batch", d.ID, d.ThreadID, d.TurnID, "completed"))
	}
	if _, _, dispatch, err := f.s.BeginTurnDelivery(t.Context(), f.workspace, "main", input); err != nil || !dispatch {
		t.Fatal("waiver dispatch", dispatch, err)
	}
	_, err := f.s.RecordDelivery(t.Context(), f.workspace, "main", input.ID, "delivered", "corrections", "")
	f.ok(err)
	f.s = &Store{Directory: f.s.Directory}
	if _, _, dispatch, err := f.s.BeginTurnDelivery(t.Context(), f.workspace, "main", input); err != nil || dispatch {
		t.Fatal("waiver replay", dispatch, err)
	}
	changed := input
	changed.ReviewWaiver = "different authorization"
	if _, _, _, err := f.s.BeginTurnDelivery(t.Context(), f.workspace, "main", changed); err == nil {
		t.Fatal("changed waiver reused delivery identity")
	}
	f.ok(f.s.ResumeWorkflow(t.Context(), f.workspace, "main", "batch"))
	f.ok(f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{f.batch.Base}, "corrections complete"))
	w := f.state()
	if w.ReviewWaiver != input.ReviewWaiver || w.Phase != PhaseValidation || len(w.Reviews) != 2 || w.Reviews[0].Verdict != VerdictFindings || len(w.Checks) != 0 {
		t.Fatal("waiver lost evidence or skipped validation", w)
	}
	if err := f.s.RecordWorkflowCheck(t.Context(), f.workspace, "main", "batch", check); err == nil {
		t.Fatal("waiver reused old validation")
	}
	check.Generation = w.Generation
	f.check(check)
	if f.state().Phase != PhaseReady {
		t.Fatal("waiver did not reach readiness")
	}
	if _, _, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "unwanted", w.Generation, DispatchCorrectness); err == nil {
		t.Fatal("waiver dispatched another review")
	}
	proof, err := f.s.RecordIntegration(t.Context(), f.workspace, "main", "batch", "implementer")
	f.ok(err)
	b := f.batch
	b.Workflow = f.state()
	if err := workflowReady(b, strings.Repeat("f", 40)); err == nil {
		t.Fatal("waiver approved another revision")
	}
	b.Workflow.ReviewWaiver = ""
	if err := workflowReady(b, f.batch.Base); err == nil {
		t.Fatal("findings counted as normal review clearance")
	}
	removed, err := f.s.Cleanup(t.Context(), f.workspace, "main", "batch", proof, func(publish func() error) error { return publish() })
	f.ok(err)
	if removed.State != "removed" || removed.Workflow.ReviewWaiver != input.ReviewWaiver || removed.Workflow.Reviews[0].Verdict != VerdictFindings {
		t.Fatal("cleanup lost the waiver or original finding", removed.Workflow)
	}
}

func TestWorkflowDispatchRecoveryAndStops(t *testing.T) {
	f := newWorkflowFixture(t, nil)
	d, dispatch, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "review", 1, DispatchCorrectness)
	f.ok(err)
	if !dispatch {
		t.Fatal("no initial dispatch")
	}
	f.s = &Store{Directory: f.s.Directory}
	for _, id := range []string{"review", "another-call"} {
		_, again, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", id, 1, DispatchCorrectness)
		f.ok(err)
		if again {
			t.Fatal("uncertain launch repeated")
		}
	}
	f.ok(f.s.FailWorkflowDispatch(t.Context(), f.workspace, "main", "batch", d.ID, true, "connection lost"))
	if _, _, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "second", 1, DispatchSimplification); err == nil {
		t.Fatal("unreconciled dispatch bypassed")
	}
	f.ok(f.s.RecordWorkflowDispatch(t.Context(), f.workspace, "main", "batch", d.ID, "reviewer", "turn"))
	if err := f.s.RecordWorkflowDispatch(t.Context(), f.workspace, "main", "batch", d.ID, "other", "turn"); err == nil {
		t.Fatal("ack identity replaced")
	}
	second, _, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "second", 1, DispatchSimplification)
	f.ok(err)
	if err := f.s.RecordWorkflowDispatch(t.Context(), f.workspace, "main", "batch", second.ID, "reviewer", "other"); err == nil {
		t.Fatal("reviews shared thread")
	}
	if err := f.s.RecordWorkflowDispatch(t.Context(), f.workspace, "main", "batch", second.ID, "implementer", "other"); err == nil {
		t.Fatal("implementer reviewed itself")
	}
	f.ok(f.s.StopWorkflow(t.Context(), f.workspace, "main", "batch", PhaseCanceled, "user stopped"))
	f.ok(f.s.RecordWorkflowDispatch(t.Context(), f.workspace, "main", "batch", second.ID, "reviewer-2", "late"))
	if f.state().Phase != PhaseCanceled {
		t.Fatal("late ack revived workflow")
	}
	if err := f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{strings.Repeat("a", 40)}, "report"); err == nil {
		t.Fatal("canceled workflow resumed")
	}
}

func TestWorkflowReviewerMissingAcknowledgementAcrossGenerations(t *testing.T) {
	f := newWorkflowFixture(t, nil)
	d, launch, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "pending", 1, DispatchCorrectness)
	f.ok(err)
	if !launch || d.ThreadID != "" {
		t.Fatal("fixture requires an unacknowledged initial launch")
	}
	f.ok(f.s.StopWorkflow(t.Context(), f.workspace, "main", "batch", PhaseCanceled, "stop before acknowledgement"))
	f.ok(f.s.ResumeWorkflow(t.Context(), f.workspace, "main", "batch"))
	f.ok(f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{f.batch.Base}, "new input"))
	f.s = &Store{Directory: f.s.Directory}
	for _, uncertain := range []bool{false, true} {
		if uncertain {
			f.ok(f.s.FailWorkflowDispatch(t.Context(), f.workspace, "main", "batch", d.ID, true, "connection lost"))
		}
		if _, again, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "next", f.state().Generation, DispatchCorrectness); err == nil || again {
			t.Fatal("missing acknowledgement permitted a replacement reviewer", uncertain)
		}
	}
	f.ok(f.s.RecordWorkflowDispatch(t.Context(), f.workspace, "main", "batch", d.ID, "recovered", "old-turn"))
	f.ok(f.s.RecordWorkflowCompletion(t.Context(), f.workspace, "main", "batch", d.ID, "recovered", "old-turn", "interrupted"))
	next, launch, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "next", f.state().Generation, DispatchCorrectness)
	f.ok(err)
	if !launch || next.ThreadID != "recovered" || len(f.state().Dispatches) != 2 {
		t.Fatal("reconciled launch did not reuse its recovered identity", next)
	}
}

func TestWorkflowStrictResults(t *testing.T) {
	base := ReviewResult{Revision: strings.Repeat("a", 40), Purpose: ReviewCorrectness, Verdict: VerdictClear, Findings: []ReviewFinding{}}
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReviewResult(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{}`, `null`, strings.Replace(string(raw), `"reason":""`, `"extra":true`, 1), strings.Replace(string(raw), `"findings":[]`, `"findings":null`, 1), strings.Replace(string(raw), `"clear"`, `"approved"`, 1), strings.Replace(string(raw), `"findings":[]`, `"findings":[{"id":"F1","detail":"x"}]`, 1), strings.Replace(string(raw), `"reason":""`, `"reason":null`, 1)} {
		if _, err := DecodeReviewResult([]byte(bad)); err == nil {
			t.Fatalf("malformed review accepted: %s", bad)
		}
	}
	f := newWorkflowFixture(t, nil)
	d := f.dispatch(DispatchCorrectness)
	base.Revision = strings.Repeat("b", 40)
	if err := f.s.RecordWorkflowReview(t.Context(), f.workspace, "main", "batch", d.ID, HostReceipt{d.ThreadID, d.TurnID, "result"}, base); err == nil {
		t.Fatal("wrong-revision review accepted")
	}
	if f.state().Phase != PhaseReview || len(f.state().Reviews) != 0 {
		t.Fatal("bad verdict changed state")
	}
	if _, err := DecodeCandidate([]byte(`{"revision":"HEAD"}`)); err == nil {
		t.Fatal("mutable candidate accepted")
	}
}

func TestWorkflowFailedChecksAndBlockedReview(t *testing.T) {
	for _, waived := range []bool{false, true} {
		for _, outcome := range []CheckOutcome{CheckFailed, CheckUncertain} {
			t.Run(fmt.Sprintf("%s/waived=%v", outcome, waived), func(t *testing.T) {
				f := newWorkflowFixture(t, []RequiredCheck{{"check", "command"}})
				if waived {
					f.ok(f.s.StopWorkflow(t.Context(), f.workspace, "main", "batch", PhaseCanceled, "user stopped"))
					f.ok(f.s.ObserveLaunch(t.Context(), f.workspace, "main", "batch", "completed", ""))
					_, _, _, err := f.s.BeginTurnDelivery(t.Context(), f.workspace, "main", Delivery{ID: "waive", From: "main", Target: "implementer", Message: "finish", ReviewWaiver: "user requested"})
					f.ok(err)
					f.ok(f.s.ResumeWorkflow(t.Context(), f.workspace, "main", "batch"))
					f.ok(f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{f.batch.Base}, "done"))
				}
				var exit *int
				if outcome == CheckFailed {
					code := 1
					exit = &code
				}
				f.check(CheckResult{Generation: f.state().Generation, Phase: PhaseValidation, Command: "command", Cwd: f.batch.Cwd, Revision: f.batch.Base, CheckID: "check", Outcome: outcome, ExitCode: exit, Output: "tail"})
				want := PhaseNeedsDecision
				if outcome == CheckFailed {
					want = PhaseCorrection
				}
				if w := f.state(); w.Phase != want || w.Reason == "" {
					t.Fatal("bad check was not routed", w)
				}
			})
		}
	}
	f := newWorkflowFixture(t, nil)
	f.review(DispatchCorrectness, VerdictBlocked)
	f.review(DispatchSimplification, VerdictFindings)
	if f.state().Phase != PhaseNeedsDecision {
		t.Fatal("blocked review produced correction")
	}
}

func TestWorkflowLegacyAndDiagnosis(t *testing.T) {
	s, workspace, b := launchFixture(t)
	_, _, err := s.BeginLaunch(t.Context(), workspace, "main", "batch", "legacy", jsontext.Value(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordThread(t.Context(), workspace, "main", "batch", "child"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordIntegration(t.Context(), workspace, "main", "batch", "child"); err != nil {
		t.Fatal("legacy integration changed", err)
	}
	if err := s.ConfigureWorkflow(t.Context(), workspace, "main", "batch", WorkflowAssignment{Kind: WorkflowImplementation, Scope: "late"}); err == nil {
		t.Fatal("workflow configured after launch")
	}
	s, workspace, b = launchFixture(t)
	if err := s.ConfigureWorkflow(t.Context(), workspace, "main", "batch", WorkflowAssignment{Kind: WorkflowDiagnosis, Scope: "read only"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AdmitCandidate(t.Context(), workspace, "main", "batch", Candidate{b.Base}, "report"); err == nil {
		t.Fatal("diagnosis entered review")
	}
}

func TestWorkflowManifestReadFailureDoesNotGrantDispatch(t *testing.T) {
	f := newWorkflowFixture(t, nil)
	path, err := f.s.runPath(f.workspace, "main")
	f.ok(err)
	// An unavailable retained manifest must never grant host dispatch.
	saved, err := os.ReadFile(path)
	f.ok(err)
	f.ok(os.Remove(path))
	f.ok(os.Mkdir(path, 0700))
	_, dispatch, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "review", 1, DispatchCorrectness)
	if err == nil || dispatch {
		t.Fatal("failed storage granted launch")
	}
	f.ok(os.Remove(path))
	f.ok(os.WriteFile(path, saved, 0600))
	if f.state().Phase != PhaseQueuedReview {
		t.Fatal("storage failure changed retained state")
	}
}

func TestWorkflowShadowAdmission(t *testing.T) {
	// Empty snapshots are sufficient to exercise the real shadow preparation,
	// plan, writeback, and proof owners without a second snapshot implementation.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	workspace := t.TempDir()
	snapshot := func(ctx context.Context, source, repo string) (string, error) {
		if _, err := os.Stat(repo); os.IsNotExist(err) {
			if _, err := git(ctx, source, "init", "--bare", "-q", repo); err != nil {
				return "", err
			}
		}
		tree, err := shadowGit(ctx, repo, "mktree")
		if err != nil {
			return "", err
		}
		commit, err := shadowGit(ctx, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit-tree", strings.TrimSpace(string(tree)), "-m", "snapshot")
		if err != nil {
			return "", err
		}
		tip := strings.TrimSpace(string(commit))
		_, err = shadowGit(ctx, repo, "update-ref", "refs/snapshot", tip)
		return tip, err
	}
	s := &Store{Directory: t.TempDir(), ShadowSnapshot: snapshot}
	b, err := s.Prepare(t.Context(), workspace, "main", "batch")
	if err != nil {
		t.Fatal(err)
	}
	f := &workflowFixture{t, s, workspace, b}
	f.ok(s.ConfigureWorkflow(t.Context(), workspace, "main", "batch", WorkflowAssignment{Kind: WorkflowImplementation, Scope: "shadow change"}))
	_, _, err = s.BeginLaunch(t.Context(), workspace, "main", "batch", "work", jsontext.Value(`{}`), nil)
	f.ok(err)
	f.ok(s.RecordThread(t.Context(), workspace, "main", "batch", "implementer"))
	f.ok(s.RecordTurn(t.Context(), workspace, "main", "batch", "implementer", "initial"))
	f.ok(s.AdmitCandidate(t.Context(), workspace, "main", "batch", Candidate{b.Base}, "report"))
	if _, err = s.IntegrateShadow(t.Context(), workspace, "main", "batch", "implementer"); err == nil {
		t.Fatal("unreviewed shadow writeback admitted")
	}
	f.review(DispatchCorrectness, VerdictClear)
	f.review(DispatchSimplification, VerdictClear)
	_, err = s.IntegrateShadow(t.Context(), workspace, "main", "batch", "implementer")
	f.ok(err)
	_, err = s.RecordIntegration(t.Context(), workspace, "main", "batch", "implementer")
	f.ok(err)
	_, err = git(t.Context(), b.Cwd, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "changed")
	f.ok(err)
	if _, err = s.IntegrateShadow(t.Context(), workspace, "main", "batch", "implementer"); err == nil {
		t.Fatal("changed shadow tip admitted")
	}
}

func TestWorkflowRejectedResultsRouteCorrectionAndInputResumes(t *testing.T) {
	f := newWorkflowFixture(t, nil)
	d, _, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "lost", 1, DispatchCorrectness)
	f.ok(err)
	f.ok(f.s.FailWorkflowDispatch(t.Context(), f.workspace, "main", "batch", d.ID, true, "connection lost"))
	f.ok(f.s.StopWorkflow(t.Context(), f.workspace, "main", "batch", PhaseNeedsDecision, "connection lost"))
	f.ok(f.s.ResumeWorkflow(t.Context(), f.workspace, "main", "batch"))
	f.ok(f.s.ResumeWorkflow(t.Context(), f.workspace, "main", "batch"))
	if w := f.state(); w.Phase != PhaseImplementation || w.Generation != 2 || w.Reason != "" {
		t.Fatal("new input did not resume implementation", w)
	}
	// An implementation result that cannot become a candidate is corrected.
	f.ok(f.s.RouteWorkflowCorrection(t.Context(), f.workspace, "main", "batch", "checkout has uncommitted changes", "done"))
	for round := 1; round <= 2; round++ {
		if w := f.state(); w.Phase != PhaseCorrection || w.Report != "done" || w.Reason != "checkout has uncommitted changes" {
			t.Fatal("rejected result was not routed to correction", w)
		}
		f.dispatch(DispatchCorrection)
		f.ok(f.s.RouteWorkflowCorrection(t.Context(), f.workspace, "main", "batch", "checkout has uncommitted changes", "done"))
	}
	if w := f.state(); w.Phase != PhaseNeedsDecision || w.CorrectionRounds != 2 || !strings.HasSuffix(w.Reason, "after two correction rounds") {
		t.Fatal("correction budget not enforced", w)
	}
	f.ok(f.s.ResumeWorkflow(t.Context(), f.workspace, "main", "batch"))
	f.ok(f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{f.batch.Base}, "report"))
	// A new generation does not settle a missing host acknowledgement.
	if _, dispatch, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "fresh", f.state().Generation, DispatchCorrectness); err == nil || dispatch {
		t.Fatal("superseded uncertain launch permitted a replacement reviewer")
	}
	f.ok(f.s.FailWorkflowDispatch(t.Context(), f.workspace, "main", "batch", d.ID, false, "host confirmed launch rejected"))
	if _, dispatch, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "fresh", f.state().Generation, DispatchCorrectness); err != nil || !dispatch {
		t.Fatal("confirmed no-effect rejection blocked a new generation", err)
	}
	if w := f.state(); w.CorrectionRounds != 0 {
		t.Fatal("resume kept the old correction budget", w)
	}
}

func TestWorkflowBlockedCandidateRetainsReport(t *testing.T) {
	f := newWorkflowFixture(t, nil)
	if err := f.s.BlockWorkflowCandidate(t.Context(), f.workspace, "main", "batch", "blocked /1", "report"); err == nil {
		t.Fatal("blocker replaced an admitted candidate")
	}
	f.ok(f.s.StopWorkflow(t.Context(), f.workspace, "main", "batch", PhaseNeedsDecision, "decision"))
	f.ok(f.s.ResumeWorkflow(t.Context(), f.workspace, "main", "batch"))
	f.ok(f.s.BlockWorkflowCandidate(t.Context(), f.workspace, "main", "batch", "blocked /1", "current report"))
	if w := f.state(); w.Phase != PhaseNeedsDecision || w.Reason != "blocked /1" || w.Report != "current report" || w.CorrectionRounds != 0 {
		t.Fatal("blocker or report not durable", w)
	}
	f.ok(f.s.ResumeWorkflow(t.Context(), f.workspace, "main", "batch"))
	f.ok(f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{f.batch.Base}, "resolved"))
	if w := f.state(); w.Phase != PhaseQueuedReview || w.Reason != "" || w.Report != "resolved" {
		t.Fatal("historical blocker prevented later admission", w)
	}
}
