package router

import (
	"context"
	"encoding/json/jsontext"
	"testing"
	"time"
)

func TestAppServerOrchestrateIndependentBatchProgressCleanupArchive(t *testing.T) {
	u := orchestrateCleanupUI(t)
	p, workspace := u.proxy, u.session.cwd
	s := p.orchestration.store
	other, err := s.Prepare(t.Context(), workspace, "main", "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginLaunch(t.Context(), workspace, "main", "other", "work", jsontext.Value(`{}`), nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		s.RecordThread(t.Context(), workspace, "main", "other", "other-child"),
		s.RecordTurn(t.Context(), workspace, "main", "other", "other-child", "other-turn"),
		p.journals.initialize(u.ctx, p.replayStore, other.Cwd, "other-child", "/root", ""),
		p.journals.bindIdentity(u.ctx, p.replayStore, other.Cwd, "other-child", "", "/root", true),
		p.journals.bindRun(u.ctx, p.replayStore, other.Cwd, "other-child", journalRun{Directory: s.Directory, Workspace: workspace, Main: "main"}),
		p.journals.observeLifecycle(u.ctx, p.replayStore, other.Cwd, "other-child", "done", ""),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Other batch"), Agent: "/root/other", State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.applyJournal(u.ctx, workspace, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}, {Op: "set", P: "/2", State: new("accepted")}}); err != nil {
		t.Fatal(err)
	}
	authorize := func(ctx context.Context, name string, publish func() error) error {
		_, err := p.orchestrateCleanupProof(ctx, workspace, "main", name, publish)
		return err
	}
	proofOther, err := p.orchestrateCleanupProof(u.ctx, workspace, "main", "other", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cleanup(u.ctx, workspace, "main", "other", proofOther, func(publish func() error) error { return authorize(u.ctx, "other", publish) }); err != nil {
		t.Fatal(err)
	}
	proof, err := p.orchestrateCleanupProof(u.ctx, workspace, "main", "batch", nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanupAuthorized, archiveAtAuthorize := make(chan struct{}), make(chan struct{})
	cleanupDone, archiveDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := s.Cleanup(u.ctx, workspace, "main", "batch", proof, func(publish func() error) error {
			return authorize(u.ctx, "batch", func() error {
				close(cleanupAuthorized)
				select {
				case <-archiveAtAuthorize:
				case <-time.After(2 * time.Second):
					return context.DeadlineExceeded
				}
				return publish()
			})
		})
		cleanupDone <- err
	}()
	select {
	case <-cleanupAuthorized:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not reach authorized intent")
	}
	ctx, cancel := context.WithTimeout(u.ctx, 300*time.Millisecond)
	defer cancel()
	go func() {
		_, err := s.BeginArchive(ctx, workspace, "main", "other", func(publish func() error) error {
			close(archiveAtAuthorize)
			return authorize(ctx, "other", publish)
		})
		archiveDone <- err
	}()
	select {
	case err := <-archiveDone:
		if err != nil {
			t.Errorf("independent archive stalled behind cleanup publication: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("archive did not terminate")
	}
	select {
	case err := <-cleanupDone:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup remained blocked")
	}
}
