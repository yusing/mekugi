package router

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/yusing/mekugi/internal/appserver"
	"path/filepath"
	"testing"
	"time"
)

func drainOrchestrateWork(t *testing.T, u *appServerUI) {
	t.Helper()
	for len(u.orchestrateJobs) != 0 {
		select {
		case complete := <-u.orchestrateCompletions:
			u.completeOrchestrateWork(complete)
		case <-time.After(5 * time.Second):
			t.Fatal("orchestration storage completion timeout")
		}
	}
}
func orchestrateTestReply(t *testing.T, u *appServerUI, request btwTestRPC, response string) {
	t.Helper()
	btwTestReply(t, u, request, response)
	drainOrchestrateWork(t, u)
}
func orchestrateTestMessage(t *testing.T, u *appServerUI, message string) {
	t.Helper()
	appServerTestMessage(t, u, message)
	drainOrchestrateWork(t, u)
}

func TestAppServerOrchestrateStorageContention(t *testing.T) {
	u, request := orchestrateIdentityPendingTurn(t)
	store := u.proxy.orchestration.store
	workspace := u.session.cwd
	path := filepath.Join(store.Directory, fmt.Sprintf("%x", sha256.Sum256([]byte(workspace))), fmt.Sprintf("%x.json.lock", sha256.Sum256([]byte("main"))))
	lock := flock.New(path)
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	command := &orchestrateCommand{ctx: ctx, workspace: workspace, main: "main", input: orchestrateSpawnInput{TaskName: "batch", Message: "work"}, reply: make(chan orchestrateResult, 1)}
	returned := make(chan struct{})
	go func() {
		u.startOrchestratedChild(command)
		// Both acknowledgement and lifecycle persistence must also stay off the UI.
		btwTestReply(t, u, request, `{"turn":{"id":"running-turn"}}`)
		u.orchestrateMessage(appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"child","turn":{"id":"running-turn","status":"completed"}}`)})
		if _, err := u.key('x'); err != nil {
			t.Error(err)
		}
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		lock.Unlock()
		t.Fatal("UI blocked on orchestration storage")
	}
	if !u.orchestrateBusy() {
		t.Fatal("pending storage lost departure protection")
	}
	select {
	case <-command.reply:
		t.Fatal("launch replied before persistence")
	default:
	}
	cancel()
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	drainOrchestrateWork(t, u)
	result := <-command.reply
	if result.err == nil {
		t.Fatal("canceled launch dispatched")
	}
	batches, err := store.List(t.Context(), workspace, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range batches {
		if b.TaskName == "batch" && (b.Launch.TurnID != "running-turn" || b.Launch.HostStatus != "completed") {
			t.Fatal("out-of-order retained lifecycle", b)
		}
	}
}
func TestAppServerOrchestrateInterruptAckBeforePersistence(t *testing.T) {
	u, request := orchestrateIdentityPendingTurn(t)
	child := u.orchestrateThreads["child"]
	ctx, cancel := context.WithCancel(t.Context())
	child.command.ctx = ctx
	cancel()
	// Leave the persistence completion unconsumed, just as a busy UI select can.
	btwTestReply(t, u, request, `{"turn":{"id":"running-turn"}}`)
	var interruptID string
	for id, rpc := range u.orchestrateRequests {
		if rpc.method == "turn/interrupt" {
			interruptID = id
		}
	}
	if interruptID == "" {
		t.Fatal("no interrupt")
	}
	u.orchestrateMessage(appserver.Message{ID: []byte(interruptID), Result: []byte("{}")})
	select {
	case result := <-child.command.reply:
		if result.err == nil {
			t.Errorf("successful spawn reply before RecordTurn completion: state=%s pending_jobs=%d", result.batch.State, len(u.orchestrateJobs))
		}
	default:
	}
	drainOrchestrateWork(t, u)
}

func TestAppServerOrchestrateShutdownRetainsOutcome(t *testing.T) {
	u, request := orchestrateIdentityPendingTurn(t)
	btwTestReply(t, u, request, `{"turn":{"id":"running-turn"}}`)
	u.orchestrateMessage(appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"child","turn":{"id":"running-turn","status":"completed"}}`)})
	if err := u.closeOrchestrateStorage(); err != nil {
		t.Fatal(err)
	}
	batches, err := u.proxy.orchestration.store.List(t.Context(), u.session.cwd, "main")
	if err != nil {
		t.Fatal(err)
	}
	if batches[0].Launch.TurnID != "running-turn" || batches[0].Launch.HostStatus != "completed" {
		t.Fatal("shutdown lost queued outcome", batches[0])
	}
}
