package orchestrate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkflowReviewStorageOwnership(t *testing.T) {
	f := newWorkflowFixture(t, nil)
	var scratch []string
	for _, kind := range []WorkflowDispatchKind{DispatchCorrectness, DispatchSimplification} {
		id := string(kind)
		_, _, err := f.s.BeginWorkflowDispatch(t.Context(), f.workspace, "main", "batch", id, f.state().Generation, kind)
		f.ok(err)
		path, err := f.s.PrepareWorkflowReviewStorage(t.Context(), f.workspace, "main", "batch", id)
		f.ok(err)
		scratch = append(scratch, path)
		if filepath.Dir(path) != filepath.Dir(f.batch.Checkout) || path == f.batch.Checkout {
			t.Fatal("scratch is not outside the checkout", path)
		}

	}
	if scratch[0] == scratch[1] {
		t.Fatal("reviewers share scratch")
	}
	// Reopen ownership from disk. Internal links must not authorize deletion of
	// the candidate or the other reviewer's still-live files.
	f.ok(os.Symlink(f.batch.Checkout, filepath.Join(scratch[0], "source")))
	f.ok(os.WriteFile(filepath.Join(scratch[1], "live"), []byte("owned"), 0600))
	f.s = &Store{Directory: f.s.Directory}
	if err := f.s.ReleaseWorkflowReviewStorage(t.Context(), f.workspace, "main", "batch", string(DispatchCorrectness), false); err == nil {
		t.Fatal("unconfirmed reviewer storage was released")
	}
	f.ok(f.s.FailWorkflowDispatch(t.Context(), f.workspace, "main", "batch", string(DispatchCorrectness), false, "host rejected launch"))
	f.ok(f.s.ReleaseWorkflowReviewStorage(t.Context(), f.workspace, "main", "batch", string(DispatchCorrectness), false))
	f.ok(f.s.ReleaseWorkflowReviewStorage(t.Context(), f.workspace, "main", "batch", string(DispatchCorrectness), false))
	if _, err := os.Stat(scratch[0]); !os.IsNotExist(err) {
		t.Fatal("released scratch remains", err)
	}
	if _, err := os.Stat(f.batch.Checkout); err != nil {
		t.Fatal("source was removed", err)
	}
	if _, err := os.Stat(filepath.Join(scratch[1], "live")); err != nil {
		t.Fatal("other reviewer's storage was removed", err)
	}
	if err := f.s.ReleaseWorkflowReviewStorage(t.Context(), f.workspace, "other-main", "batch", string(DispatchSimplification), false); err == nil {
		t.Fatal("foreign run released storage")
	}
}
