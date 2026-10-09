package router

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/internal/appserver"
)

func TestAppServerOrchestrateWaitPersistenceAndScope(t *testing.T) {
	u, request := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, request, `{"turn":{"id":"running-turn"}}`)
	child := u.orchestrateThreads["child"]
	wait := func() *orchestrateCommand {
		c := &orchestrateCommand{ctx: t.Context(), main: "main", workspace: u.session.cwd, wait: true, reply: make(chan orchestrateResult, 1)}
		u.startOrchestratedChild(c)
		return c
	}
	c := wait()
	foreign := *child
	owner := *child.command
	owner.main = "other"
	foreign.command = &owner
	u.thread = "other"
	other := &orchestrateCommand{ctx: t.Context(), main: "other", workspace: u.session.cwd, wait: true, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(other)
	u.thread = "main"
	u.publishOrchestratedEvent(&foreign, orchestrateEvent{Task: "foreign", Kind: "done"})
	select {
	case result := <-other.reply:
		if result.event == nil || result.event.Task != "foreign" {
			t.Fatal("wrong coordinator event", result)
		}
	default:
		t.Fatal("an earlier coordinator's wait blocked this coordinator")
	}
	u.orchestrateMessage(appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"child","turn":{"id":"running-turn","status":"failed","error":{"message":"host failure"}}}`)})
	select {
	case <-c.reply:
		t.Fatal("wait published a foreign event or an unretained host outcome")
	default:
	}
	drainOrchestrateWork(t, u)
	result := <-c.reply
	if result.event == nil || result.event.Kind != "failure" || result.event.Status != "failed" || result.event.Message != "host failure" {
		t.Fatalf("wait outcome: %+v", result)
	}
	batches, err := u.proxy.orchestration.store.List(t.Context(), u.session.cwd, "main")
	if err != nil || batches[0].Error != "host failure" {
		t.Fatal("failure detail not retained", batches, err)
	}
	// A native descendant's prompt belongs to its batch, not Main's tree.
	u.session.registerThread(appServerThreadInfo{ID: "native", ParentThreadID: "child"})
	c = wait()
	u.orchestrateMessage(appserver.Message{ID: []byte(`6`), Method: "item/tool/requestUserInput", Params: []byte(`{"threadId":"main","turnId":"main-turn"}`)})
	u.orchestrateMessage(appserver.Message{Method: "item/completed", Params: []byte(`{"threadId":"child","turnId":"turn","item":{"type":"commandExecution","aggregatedOutput":"ordinary output"}}`)})
	select {
	case <-c.reply:
		t.Fatal("Main's prompt or an ordinary item woke the batch waiter")
	default:
	}
	u.orchestrateMessage(appserver.Message{ID: []byte(`7`), Method: "item/tool/requestUserInput", Params: []byte(`{"threadId":"native","turnId":"native-turn"}`)})
	if event := (<-c.reply).event; event == nil || event.Task != "batch" || event.Thread != "native" || string(event.Request) != "7" {
		t.Fatalf("descendant prompt: %+v", event)
	}
	// Persistence failure is visible to the waiter, never a false saved outcome.
	c = wait()
	u.proxy.orchestration.store.Directory = filepath.Join(t.TempDir(), "unavailable")
	if err := os.WriteFile(u.proxy.orchestration.store.Directory, []byte("block storage"), 0600); err != nil {
		t.Fatal(err)
	}
	u.orchestrateMessage(appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"child","turn":{"id":"next-turn","status":"completed"}}`)})
	drainOrchestrateWork(t, u)
	if event := (<-c.reply).event; event == nil || event.Kind != "failure" || event.Status != "storage_failed" {
		t.Fatalf("missing persistence failure: %+v", event)
	}
	c = wait()
	u.closeOrchestrateStorage()
	if result := <-c.reply; result.err != context.Canceled {
		t.Fatal("shutdown did not release live wait", result)
	}
}
