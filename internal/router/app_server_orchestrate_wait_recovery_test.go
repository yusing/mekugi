package router

import (
	"github.com/yusing/mekugi/internal/orchestrate"
	"strings"
	"testing"
	"time"
)

func TestAppServerOrchestrateWaitShutdownBeforeDispatch(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"running"}}`)
	store, workspace := u.proxy.orchestration.store, u.session.cwd
	now := time.Now()
	input := orchestrate.Wait{ID: "shutdown"}
	if _, err := store.RegisterWait(t.Context(), workspace, "main", input, now); err != nil {
		t.Fatal(err)
	}
	u.advanceOrchestratedWaits(workspace, "main", []orchestrateEvent{{Task: "batch", Kind: "approval", Item: "approval"}})
	if err := u.closeOrchestrateStorage(); err != nil {
		t.Fatal(err)
	}
	if w, err := store.RegisterWait(t.Context(), workspace, "main", input, now); err != nil || w.State != "ready" {
		t.Fatal("shutdown lost unsent result", w, err)
	}
	if u.client.Input.(*appServerTestInput).Len() != 0 {
		t.Fatal("shutdown dispatched injection")
	}
	restored, wire := resumeOrchestrationMain(t, store, workspace)
	wire.Reset() // Model and roster requests were already acknowledged by the fixture.
	drainOrchestrateWork(t, restored)
	if !strings.Contains(wire.String(), "WAIT_RESULT") {
		t.Fatal("recovery lost ready result", wire.String())
	}
	orchestrateTestReply(t, restored, btwTestRequest(t, wire, "turn/start", "main"), `{}`)
}
func TestAppServerOrchestrateWaitNoBatchRecovery(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ctx = t.Context()
	workspace := t.TempDir()
	u.session.start("main", workspace)
	u.thread = "main"
	u.ensureShell()
	store := &orchestrate.Store{Directory: t.TempDir()}
	u.proxy = &mekugiProxy{orchestration: &orchestrateRuntime{store: store}}
	t.Cleanup(func() {
		u.closeOrchestrateStorage()
		u.closeOrchestratedViews()
		u.shell.diff.close()
		u.shell.diffScreen.Close()
	})
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now.Add(time.Second) }
	if _, err := store.RegisterWait(t.Context(), workspace, "main", orchestrate.Wait{ID: "no-batch", TimeoutMS: 1}, now); err != nil {
		t.Fatal(err)
	}
	u.recoverOrchestratedRun()
	drainOrchestrateWork(t, u)
	if !strings.Contains(wire.String(), "WAIT_RESULT") {
		t.Fatal("expired retained wait ignored because run has zero batches")
	}
}
func TestAppServerOrchestrateWaitForeignRunDeadline(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"running"}}`)
	store, workspace := u.proxy.orchestration.store, u.session.cwd
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	wire := u.client.Input.(*appServerTestInput)
	for _, input := range []struct {
		main, id string
		timeout  int
	}{{"main", "own", 5}, {"foreign", "other", 20}} {
		if _, err := store.RegisterWait(t.Context(), workspace, input.main, orchestrate.Wait{ID: input.id, TimeoutMS: input.timeout}, now); err != nil {
			t.Fatal(err)
		}
		u.advanceOrchestratedWaits(workspace, input.main, nil)
		drainOrchestrateWork(t, u)
	}
	now = now.Add(5 * time.Millisecond)
	u.flushOrchestratedEvents()
	drainOrchestrateWork(t, u)
	if !strings.Contains(wire.String(), "WAIT_RESULT") {
		t.Fatalf("foreign deadline displaced active run deadline: now=%s", now)
	}
}

func TestAppServerOrchestrateWaitTransientExpiryFailure(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"running"}}`)
	store, workspace := u.proxy.orchestration.store, u.session.cwd
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	wire := u.client.Input.(*appServerTestInput)
	if _, err := store.RegisterWait(t.Context(), workspace, "main", orchestrate.Wait{ID: "expiry-failure", TimeoutMS: 5}, now); err != nil {
		t.Fatal(err)
	}
	u.advanceOrchestratedWaits(workspace, "main", nil)
	drainOrchestrateWork(t, u)
	now = now.Add(5 * time.Millisecond)
	directory := store.Directory
	store.Directory = "invalid-relative-storage"
	u.flushOrchestratedEvents()
	drainOrchestrateWork(t, u)
	store.Directory = directory
	now = now.Add(time.Second)
	u.flushOrchestratedEvents()
	drainOrchestrateWork(t, u)
	if !strings.Contains(wire.String(), "WAIT_RESULT") {
		t.Fatal("transient expiry storage failure cleared the only scheduled deadline; later ticks never retry")
	}
}

func TestAppServerOrchestrateWaitStopBeforeHostDispatch(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"running"}}`)
	store, workspace := u.proxy.orchestration.store, u.session.cwd
	input := orchestrate.Wait{ID: "stop"}
	if _, err := store.RegisterWait(t.Context(), workspace, "main", input, u.now()); err != nil {
		t.Fatal(err)
	}
	u.advanceOrchestratedWaits(workspace, "main", []orchestrateEvent{{Task: "batch", Kind: "message", Item: "selected"}})
	// The claim is saved, but its UI callback has not sent anything to Codex.
	complete := <-u.orchestrateCompletions
	u.stopOrchestratedWaits()
	u.completeOrchestrateWork(complete)
	drainOrchestrateWork(t, u)
	if w, err := store.RegisterWait(t.Context(), workspace, "main", input, u.now()); err != nil || w.State != "canceled" || u.client.Input.(*appServerTestInput).Len() != 0 {
		t.Fatal("stop dispatched a claimed but unsent result", w, err)
	}
	if w, err := store.RegisterWait(t.Context(), workspace, "main", orchestrate.Wait{ID: "next"}, u.now()); err != nil || w.State != "completed" || w.EventID == "" {
		t.Fatal("stop consumed the selected event", w, err)
	}
}
