package router

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/legacycall"

	sessionreview "github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/activity"
	"github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/changehistory"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/sessionhistory"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	changeview "github.com/yusing/mekugi/modules/changehistory/terminalview"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

// Exercise the same snapshot path used by the event-stream consumer.
func (s *mekugiReplayStore) liveDiffSnapshotFiles(ctx context.Context, scope previewmodel.Scope) ([]presentation.File, error) {
	data, err := s.liveDiffSnapshot(ctx, scope)
	if err != nil {
		return nil, err
	}
	return data.Publication.Files, nil
}

func setSavedReviewStore(ui *terminalUI, store *mekugiReplayStore) {
	ui.main.review.Projection.Changes, ui.main.review.Projection.Retained = store.changes, store.changeScope()
}

func liveDiffScopeCapture(t *testing.T, store *mekugiReplayStore, workspace, thread, call, path, before, after string) {
	t.Helper()
	id, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), workspace, thread, call)
	if err != nil {
		t.Fatal(err)
	}
	history := legacycall.History{ChangeID: id, CorrelationID: call, ReviewFiles: retainedReviewFiles([]diffmodel.ReviewFile{{BeforePath: path, AfterPath: path,
		Diff: "--- \"" + path + "\"\n+++ \"" + path + "\"\n@@ -1 +1 @@\n-" + before + "\n+" + after + "\n"}})}
	if err := store.put(t.Context(), workspace, map[string]legacycall.History{call: history}); err != nil {
		t.Fatal(err)
	}
}

func TestLiveDiffSessionScopeStreamsAndWorkspaces(t *testing.T) {
	workspace, second := t.TempDir(), t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDiffScopeCapture(t, store, workspace, "unrelated", "old", filepath.Join(workspace, "old.go"), "a", "b")
	workspaces := map[string]map[string]bool{workspace: {"current": true}}
	read := func() []presentation.File {
		t.Helper()
		// A fresh reader has no in-memory ancestry or routing cache.
		reader := testReplayStore(mekugiReplayStore{directory: store.directory})
		files, err := reader.liveDiffSnapshotFiles(t.Context(), previewmodel.Scope{Workspaces: workspaces})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	if files := read(); len(files) != 0 {
		t.Fatal("new session inherited unrelated workspace edits")
	}
	tempPath := filepath.Join(t.TempDir(), "notes.txt")
	liveDiffScopeCapture(t, store, workspace, "current", "current-call", tempPath, "old", "new")
	if files := read(); len(files) != 1 || files[0].Path != tempPath {
		t.Fatalf("outside-workspace edit missing: %#v", files)
	}
	liveDiffScopeCapture(t, store, second, "child", "child-call", filepath.Join(second, "child.txt"), "old", "new")
	workspaces[second] = map[string]bool{"child": true}
	if files := read(); len(files) != 2 {
		t.Fatalf("child/second workspace missing: %#v", files)
	}
	liveDiffScopeCapture(t, store, workspace, "unrelated", "other", filepath.Join(workspace, "other.go"), "a", "b")
	if files := read(); len(files) != 2 {
		t.Fatal("concurrent unrelated session leaked into view")
	}
}

func TestLiveDiffSessionCrossWorkspaceOverlap(t *testing.T) {
	var viewPublication presentation.Publication
	root := t.TempDir()
	first, second := filepath.Join(root, "z"), filepath.Join(root, "a")
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{first: {"root": true}, second: {"child": true}}}
	path := filepath.Join(t.TempDir(), "notes.txt")
	liveDiffScopeCapture(t, store, first, "root", "same-call-id", path, "original", "first")
	view := changeview.View{Scroll: make(map[string]int)}
	refresh := func() {
		t.Helper()
		files, err := store.liveDiffSnapshotFiles(t.Context(), scope)
		if err != nil {
			t.Fatal(err)
		}
		viewPublication = viewPublication.With(files)
		view.Update(viewPublication)
		view.RefreshVisible()
	}
	refresh()
	key := view.Files[0].Key()
	view.Scroll[key] = 2
	liveDiffScopeCapture(t, store, second, "child", "same-call-id", path, "first", "fixed")
	refresh()
	if len(view.Files) != 1 || len(view.Files[0].Chunks) != 2 || view.Files[0].Key() != key || view.Scroll[key] != 2 {
		t.Fatal("workspace switch broke shared file identity, selection or call identity")
	}
	if chunks := view.Visible[key].Chunks; len(chunks) != 1 || !strings.Contains(chunks[0].Review.Diff, "-original\n+fixed\n") {
		t.Fatalf("cross-workspace edit lost its combined result: %#v", chunks)
	}
	liveDiffScopeCapture(t, store, second, "child", "revert", path, "fixed", "original")
	refresh()
	if chunks := view.Visible[key].Chunks; len(chunks) != 0 {
		t.Fatal("cross-workspace full revert retained a net diff")
	}

}

func TestLiveDiffFreshSnapshotComposesCrossStreamCaptures(t *testing.T) {
	var viewPublication presentation.Publication
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Allocate A before B, but publish B's target edit before A's target edit.
	liveDiffScopeCapture(t, store, workspace, "A", "unrelated", filepath.Join(workspace, "other"), "old", "new")
	path := filepath.Join(workspace, "target")
	for _, capture := range []struct{ thread, call, diff string }{
		{"B", "insert", "--- target\n+++ target\n@@ -0,0 +1 @@\n+prefix\n"},
		{"A", "replace", "--- target\n+++ target\n@@ -21 +21 @@\n-old\n+new\n"},
	} {
		id, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), workspace, capture.thread, capture.call)
		if err != nil {
			t.Fatal(err)
		}
		history := legacycall.History{ChangeID: id, CorrelationID: capture.call, ReviewFiles: retainedReviewFiles([]diffmodel.ReviewFile{{BeforePath: path, AfterPath: path, Diff: capture.diff}})}
		if err := store.put(t.Context(), workspace, map[string]legacycall.History{capture.call: history}); err != nil {
			t.Fatal(err)
		}
	}
	files, err := store.liveDiffSnapshotFiles(t.Context(), previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: nil}})
	if err != nil {
		t.Fatal(err)
	}
	var view changeview.View
	viewPublication = viewPublication.With(files)
	view.Update(viewPublication)
	view.RefreshVisible()
	for _, file := range view.Files {
		if file.Path == path {
			chunks := view.Visible[file.Key()].Chunks
			text := liveDiffVisibleText(view.Visible[file.Key()])
			if len(chunks) != 2 || chunks[0].Status != "" || chunks[1].Status != "" ||
				!strings.Contains(text, "+prefix\n") || !strings.Contains(text, "@@ -20,1 +21,1 @@\n-old\n+new\n") {
				t.Fatalf("cross-stream result used stream order instead of capture order: %s", text)
			}
			return
		}
	}
	t.Fatal("target missing from snapshot")
}

func (s *mekugiReplayStore) liveDiffSnapshot(ctx context.Context, scope previewmodel.Scope) (*presentation.Snapshot, error) {
	projection := sessionreview.Projection{Changes: s.changes, Retained: s.changeScope()}
	if _, err := projection.Apply(ctx, sessionreview.Event{Kind: "scope", Scope: &scope}); err != nil {
		return nil, err
	}
	return projection.Data, nil
}

func reviewSnapshotAttempt(t *testing.T, snapshot *presentation.Snapshot, workspace, call string) presentation.Attempt {
	t.Helper()
	for attempt := range snapshot.Attempts() {
		if attempt.Key == (presentation.AttemptKey{Workspace: workspace, Call: call}) {
			return attempt
		}
	}
	t.Fatalf("missing attempt %q in %q", call, workspace)
	return presentation.Attempt{}
}

func reviewFixtureSnapshot(t *testing.T, attempts ...presentation.Attempt) *presentation.Snapshot {
	t.Helper()
	builder := presentation.NewBuilder(nil)
	for _, attempt := range attempts {
		if err := builder.Add(attempt); err != nil {
			t.Fatal(err)
		}
	}
	return builder.Finish()
}

func reviewReceiptSnapshot(t *testing.T, receipts []*presentation.Receipt) *presentation.Snapshot {
	t.Helper()
	attempts := make([]presentation.Attempt, len(receipts))
	for i, receipt := range receipts {
		attempts[i] = presentation.Attempt{AttemptIdentity: presentation.AttemptIdentity{Key: presentation.AttemptKey{Workspace: "/workspace", Call: fmt.Sprint(i)}}, Receipt: receipt}
	}
	return reviewFixtureSnapshot(t, attempts...)
}

// Publish through the real descriptor collector and Activity host. Retained
// fixtures inject their selected capture instance so attachment keys agree.
func publishFixtureReview(t *testing.T, u *appServerUI) {
	t.Helper()
	var composed composition.Composition
	history := composed.Add(sessionhistory.Module, func() sessionhistory.Deps { return sessionhistory.Deps{} })
	activity := composed.Add(activity.Module, func() activity.Deps { return activity.Deps{} })
	descriptor := changehistory.Module
	source := u.reviewSource
	if source != nil {
		descriptor.New = func(changehistory.Deps, module.Slots) (*changecapture.Service, error) { return source, nil }
		// The existing fixture owns its live capture lifetime.
		descriptor.Start, descriptor.Stop, descriptor.Resources = nil, nil, nil
	}
	captured := composed.Add(descriptor, func() changehistory.Deps { return changehistory.Deps{History: history.Value()} })
	runtime, err := composed.Activate(t.Context(), new(binding.Registry))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	u.activity = activity.Value()
	u.reviewSource = captured.Value()
	u.reviewSource.PublishReview(u.attached.session.Scope(), u.review.Projection.Data, u.attached.cwd, u.review.Projection.Coverage, u.review.Failure)
}

func fixtureActivityWithoutDetails(t *testing.T) *feed.Service {
	t.Helper()
	var composed composition.Composition
	service := composed.Add(activity.Module, func() activity.Deps { return activity.Deps{} })
	runtime, err := composed.Activate(t.Context(), new(binding.Registry))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return service.Value()
}
