package capture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestDiffReviewPublicationPreservesRetainedEvidence(t *testing.T) {
	service := new(Service)
	store := new(scope.Store)
	publication := (presentation.Publication{}).With(presentation.GroupCaptures([]presentation.Chunk{
		{Key: "temporary", Workspace: "/workspace", Review: diffmodel.ReviewFile{AfterPath: "/tmp/worktree/file.go", Diff: "temporary evidence"}},
		{Key: "project", Workspace: "/workspace", Review: diffmodel.ReviewFile{AfterPath: "/workspace/file.go", Diff: "project evidence"}},
	}))
	service.PublishReview(store, &presentation.Snapshot{Publication: publication}, "/workspace", "complete", "")
	full, pane := service.ReviewView(store), service.DiffReviewView(store)
	if len(full.Files) != 2 || len(pane.Files) != 1 || pane.Files[0].Path != "/workspace/file.go" || pane.Revision != full.Revision {
		t.Fatal("pane projection changed retained evidence or exposed hidden captures")
	}
	again := service.DiffReviewView(store)
	if &again.Files[0] != &pane.Files[0] {
		t.Fatal("unchanged owner re-materialized growing review history")
	}
	service.PublishReview(store, nil, "/workspace", "loading", "")
	if next := service.DiffReviewView(store); len(next.Files) != 0 || next.Revision == pane.Revision || next.Coverage != "loading" {
		t.Fatal("new owner revision retained the preceding pane")
	}
}

func TestServiceOwnsSelectedCheckpointLifetime(t *testing.T) {
	isolateSnapshotGit(t)
	history, err := history.NewService(nil)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "retained")
	service, err := New(history, directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Checkpoints(directory); err == nil {
		t.Fatal("capture started in constructor")
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("constructor touched storage: %v", err)
	}
	if err := service.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	if _, err := service.Checkpoints(t.TempDir()); err == nil {
		t.Fatal("borrowed another retained directory")
	}
	if _, err := service.Checkpoints("relative"); err == nil {
		t.Fatal("borrowed relative retained authority")
	}
	snapshots, err := service.Checkpoints(directory)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "source.txt"), "before\n")
	if snapshots.Checkpoint(t.Context(), workspace) == "" {
		t.Fatal("started service did not capture workspace")
	}
	// Block a real warm-up behind the checkpoint gate. Stop must cancel and join
	// it, rather than returning while a worker can still write the private store.
	repo := snapshots.repo(workspace)
	repo.gate <- struct{}{}
	defer func() { <-repo.gate }()
	repo.startWarm()
	repo.mu.Lock()
	ready := repo.ready
	repo.mu.Unlock()
	select {
	case <-ready:
		t.Fatal("warm-up passed held gate")
	default:
	}
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	default:
		t.Fatal("stop did not join warm-up")
	}
	if snapshots.ctx.Err() != context.Canceled {
		t.Fatal("stop did not cancel warm-up context")
	}
	if _, err := service.Checkpoints(directory); err == nil {
		t.Fatal("stopped owner remains available")
	}
}

func TestServiceRetainedOnlyAndCanceledStartup(t *testing.T) {
	history, err := history.NewService(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(history, "relative"); err == nil {
		t.Fatal("relative capture directory accepted")
	}
	service, err := New(history, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshots, err := service.Checkpoints(t.TempDir())
	if err != nil || snapshots != nil {
		t.Fatalf("retained-only resources: %v %v", snapshots, err)
	}
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	service, err = New(history, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := service.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start: %v", err)
	}
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
