package orchestrate

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
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
	thread := id
	if kind == DispatchCorrection {
		thread = "implementer"
	}
	f.ok(f.s.RecordWorkflowDispatch(f.t.Context(), f.workspace, "main", "batch", id, thread, ""))
	f.ok(f.s.RecordWorkflowDispatch(f.t.Context(), f.workspace, "main", "batch", id, thread, id+"-turn"))
	d.ThreadID, d.TurnID = thread, id+"-turn"
	return d
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
	wrong.Command = "another command"
	if err := f.s.RecordWorkflowCheck(t.Context(), f.workspace, "main", "batch", wrong); err == nil {
		t.Fatal("undeclared command supplied check evidence")
	}
	f.ok(f.s.RecordWorkflowCheck(t.Context(), f.workspace, "main", "batch", r))
	f.ok(f.s.RecordWorkflowCheck(t.Context(), f.workspace, "main", "batch", r))
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
		f.dispatch(DispatchCorrection)
		// A dispute may keep the commit but must invalidate both previous approvals.
		f.ok(f.s.AdmitCandidate(t.Context(), f.workspace, "main", "batch", Candidate{f.batch.Base}, "report"))
		if len(f.state().Reviews) != 0 {
			t.Fatal("dispute reused approvals")
		}
	}
	if _, _, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "extra", 3, DispatchCorrection); err == nil {
		t.Fatal("third correction dispatched")
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
	for _, outcome := range []CheckOutcome{CheckFailed, CheckUncertain} {
		t.Run(string(outcome), func(t *testing.T) {
			f := newWorkflowFixture(t, []RequiredCheck{{"check", "command"}})
			var exit *int
			if outcome == CheckFailed {
				code := 1
				exit = &code
			}
			f.ok(f.s.RecordWorkflowCheck(t.Context(), f.workspace, "main", "batch", CheckResult{Generation: 1, Phase: PhaseValidation, Command: "command", Cwd: f.batch.Cwd, Revision: f.batch.Base, CheckID: "check", Outcome: outcome, ExitCode: exit, Output: "tail"}))
			want := PhaseNeedsDecision
			if outcome == CheckFailed {
				want = PhaseCorrection
			}
			if w := f.state(); w.Phase != want || w.Reason == "" {
				t.Fatal("bad check was not routed", w)
			}
		})
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
	// The earlier uncertain review belongs to a superseded generation.
	if _, dispatch, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", "fresh", f.state().Generation, DispatchCorrectness); err != nil || !dispatch {
		t.Fatal("stale uncertainty blocked a new generation", err)
	}
	if w := f.state(); w.CorrectionRounds != 0 {
		t.Fatal("resume kept the old correction budget", w)
	}
}
