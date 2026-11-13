package executionobservation

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestNativeChildAdmissionMakesFirstCaptureReadAvailable(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-admission")
	id := f.reserve(t, f.thread, "edit")
	f.publish(t, id, "edit", "edit-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("file.txt", "file.txt", "before\n", "after\n")}})
	p := newManagedMekugiProxy(t)
	setTestProxyStore(p, f.store)
	p.Retained.Writes = new(persistence.Counter)
	reader := &completionReads{Service: p.Changes}
	p.Changes = reader
	child := f.thread + "-child"
	input := appserver.ChildHostAdmissionRequest{RootThreadID: f.thread, ParentThreadID: f.thread, ThreadID: child, LocalCwd: &f.workspace}
	if err := p.AdmitNativeChild(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	written := p.Retained.Writes.Snapshot().Bytes
	// Retrying a committed callback after a lost acknowledgment uses the same
	// stored identity, even through a newly constructed resource adapter.
	retry := New(p.Config)
	if err := retry.AdmitNativeChild(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if p.Retained.Writes.Snapshot().Bytes != written || reader.reads.Load() != 0 {
		t.Fatal("admission retry republished storage or acquired source bodies")
	}
	if p.Review.WorkspaceForThread(child) != f.workspace || len(p.Windows.local.windows) != 0 {
		t.Fatal("native cwd was not bound or admission manufactured an execution window")
	}
	body, err := p.PrepareNativeFinalCapture(t.Context(), appserver.FinalCaptureRequest{RootThreadID: f.thread, ThreadID: f.thread, RecipientThreadID: child, RecipientCwd: &f.workspace, Sources: []string{"mchanges:" + id}})
	if err != nil || body == nil || !strings.Contains(body.FullText, "+after") || reader.reads.Load() != 1 {
		t.Fatalf("first child read could not use shared retained evidence: %v, %v", body, err)
	}
	// Fresh and full-history native children use this same resource transaction;
	// native history copying must not create a separate retained namespace.
	var workers sync.WaitGroup
	for _, suffix := range []string{"-fresh", "-full-history"} {
		workers.Go(func() {
			input := appserver.ChildHostAdmissionRequest{RootThreadID: f.thread, ParentThreadID: child, ThreadID: child + suffix}
			if err := p.AdmitNativeChild(t.Context(), input); err != nil {
				t.Error(err)
				return
			}
			scope, err := p.Retained.Store.ReadAdmittedScope(input.ThreadID)
			if err != nil || scope.Namespace != f.thread || scope.Parent != child || scope.Fork != "" {
				t.Errorf("nested resource scope=%+v err=%v", scope, err)
			}
			if p.Review.WorkspaceForThread(input.ThreadID) != "" {
				t.Error("null local cwd inherited a parent's workspace")
			}
		})
	}
	workers.Wait()
}

func TestNativeChildAdmissionRejectsWithoutPublishing(t *testing.T) {
	for _, scenario := range []string{"missing root", "missing parent", "foreign parent", "conflicting child", "conflicting parent", "self parent", "invalid cwd", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			p := newManagedMekugiProxy(t)
			attachTestReplayStore(t, p)
			p.Retained.Writes = new(persistence.Counter)
			workspace := t.TempDir()
			input := appserver.ChildHostAdmissionRequest{RootThreadID: "root", ParentThreadID: "root", ThreadID: "child", LocalCwd: &workspace}
			if err := p.Retained.Store.Transaction(t.Context(), p.Retained.Writes, func(tx *history.Transaction) error {
				if err := tx.InitializeScope(history.Scope{Thread: "root", Namespace: "root"}, nil); err != nil {
					return err
				}
				if scenario == "foreign parent" {
					return tx.InitializeScope(history.Scope{Thread: "foreign", Namespace: "foreign"}, nil)
				}
				if scenario == "conflicting child" {
					return tx.InitializeScope(history.Scope{Thread: "child", Namespace: "child"}, nil)
				}
				if scenario == "conflicting parent" {
					if err := tx.InitializeScope(history.Scope{Thread: "sibling", Namespace: "root", Parent: "root"}, nil); err != nil {
						return err
					}
					return tx.InitializeScope(history.Scope{Thread: "child", Namespace: "root", Parent: "sibling"}, nil)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch scenario {
			case "missing root":
				input.RootThreadID = "missing"
			case "missing parent":
				input.ParentThreadID = "missing"
			case "foreign parent":
				input.ParentThreadID = "foreign"
			case "self parent":
				input.ParentThreadID = input.ThreadID
			case "invalid cwd":
				input.LocalCwd = new("relative")
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			prior, existed, err := p.Retained.Store.ReadScope(input.ThreadID)
			if err != nil {
				t.Fatal(err)
			}
			written := p.Retained.Writes.Snapshot().Bytes
			if err := p.AdmitNativeChild(ctx, input); err == nil {
				t.Fatal("invalid resource admission succeeded")
			}
			if p.Retained.Writes.Snapshot().Bytes != written || p.Review.WorkspaceForThread(input.ThreadID) != "" {
				t.Fatal("rejected admission mutated retained scope or review membership")
			}
			scope, found, err := p.Retained.Store.ReadScope(input.ThreadID)
			if err != nil || found != existed || scope.Namespace != prior.Namespace || scope.Parent != prior.Parent || scope.Fork != prior.Fork {
				t.Fatalf("rejected admission changed prior identity: %+v found=%v err=%v", scope, found, err)
			}
		})
	}
}
