package orchestrate

import "testing"

func TestReviewerArchiveRequiresTerminalEvidenceAndNeverReplays(t *testing.T) {
	f := newWorkflowFixture(t, nil)
	d := f.dispatch(DispatchCorrectness)
	begin := func() (bool, error) {
		return f.s.BeginReviewerArchive(t.Context(), f.workspace, "main", "batch", d.ID)
	}
	if granted, err := begin(); granted || err == nil {
		t.Fatal("active reviewer admitted to archival")
	}
	f.ok(f.s.RecordWorkflowCompletion(t.Context(), f.workspace, "main", "batch", d.ID, d.ThreadID, d.TurnID, "completed"))
	if granted, err := begin(); granted || err == nil {
		t.Fatal("completion without a retained verdict or failure admitted")
	}
	f.ok(f.s.RecordWorkflowReview(t.Context(), f.workspace, "main", "batch", d.ID, HostReceipt{d.ThreadID, d.TurnID, "verdict"}, ReviewResult{Revision: d.Revision, Purpose: ReviewCorrectness, Verdict: VerdictClear, Findings: []ReviewFinding{}}))
	granted, err := begin()
	f.ok(err)
	if !granted {
		t.Fatal("completed reviewer did not get archival")
	}
	f.s = &Store{Directory: f.s.Directory}
	if granted, err := begin(); granted || err != nil {
		t.Fatal("restart repeated uncertain archival", err)
	}
	f.ok(f.s.RecordReviewerArchive(t.Context(), f.workspace, "main", "batch", d.ID, true, nil))
	if granted, err := begin(); granted || err != nil {
		t.Fatal("archive confirmation granted another dispatch", err)
	}
	if got := f.state().Dispatches[0]; got.Archive.State != "archived" || got.HostStatus != "completed" || got.ResultItemID != "verdict" {
		t.Fatal("archival changed host result identity", got)
	}
}
