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

func TestAppServerOrchestrateJournalBlockers(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, launch := orchestrateIdentityPendingTurnWithReplay(t, replay)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
	workspace := u.orchestrateThreads["child"].batch.Cwd
	ctx, release, err := replay.beginSession(t.Context(), "child", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	wait := func(ctx context.Context) *orchestrateCommand {
		c := &orchestrateCommand{ctx: ctx, main: "main", workspace: u.session.cwd, wait: true, reply: make(chan orchestrateResult, 1)}
		u.startOrchestratedChild(c)
		return c
	}
	apply := func(thread, receipt string, mutations ...journalMutation) {
		t.Helper()
		if _, err := u.proxy.applyJournal(ctx, workspace, thread, receipt, mutations); err != nil {
			t.Fatal(err)
		}
	}
	blocked := journalMutation{Op: "add", Kind: "task", Title: new("Build batch"), State: new("blocked"), Reason: new("Missing input")}
	cancelCtx, cancel := context.WithCancel(t.Context())
	canceled := wait(cancelCtx)
	cancel()
	apply("child", "block", blocked)
	if err := u.tickOrchestratedViews(u); err != nil {
		t.Fatal(err)
	}
	if len(canceled.reply) != 0 {
		t.Fatal("canceled waiter consumed the journal event")
	}
	c := wait(t.Context())
	if event := (<-c.reply).event; event == nil || event.Kind != "blocked" || event.Task != "batch" || event.Thread != "child" || event.Path != "/1" || event.Message != "Missing input" || u.orchestrateThreads["child"].turn != "child-turn" {
		t.Fatal("unbound child blocker changed host lifecycle or lost scope", event)
	}
	// Reopening journals and replaying receipts do not repeat wait events.
	u.proxy.journals = newJournalStore()
	apply("child", "block", blocked)
	apply("child", "edit", journalMutation{Op: "set", P: "/1", Reason: new("Updated reason")})
	u.flushOrchestratedEvents()
	if len(u.orchestrateEvents) != 0 {
		t.Fatal("receipt replay or reason edit emitted another blocker")
	}
	// A new blocked state after reopening is another observation.
	apply("child", "work", journalMutation{Op: "set", P: "/1", State: new("working")})
	apply("child", "reblock", journalMutation{Op: "set", P: "/1", State: new("blocked"), Reason: new("New input")})
	c = wait(t.Context())
	if event := (<-c.reply).event; event == nil || event.Message != "New input" {
		t.Fatal("new transition was lost", event)
	}
	// A native descendant needs complete durable ancestry, not a live UI entry.
	workerCtx, workerRelease, err := replay.beginSession(t.Context(), "worker", "")
	if err != nil {
		t.Fatal(err)
	}
	defer workerRelease()
	if err := u.proxy.journals.initialize(workerCtx, replay, workspace, "worker", "/root/worker", "child"); err != nil {
		t.Fatal(err)
	}
	if err := u.proxy.journals.bindIdentity(workerCtx, replay, workspace, "worker", "child", "/root/worker", true); err != nil {
		t.Fatal(err)
	}
	if _, err := u.proxy.applyJournal(workerCtx, workspace, "worker", "block", []journalMutation{blocked}); err != nil {
		t.Fatal(err)
	}
	u.publishOrchestratedEvent(u.orchestrateThreads["child"], orchestrateEvent{Task: "batch", Thread: "child", Kind: "question"})
	c = wait(t.Context())
	if event := (<-c.reply).event; event == nil || event.Thread != "worker" || event.Task != "batch" {
		t.Fatal("native blocker was not scoped to its batch", event)
	}
	c = wait(t.Context())
	if event := (<-c.reply).event; event == nil || event.Kind != "question" {
		t.Fatal("later host event preceded the persisted blocker", event)
	}
	if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, "main", "main-block", []journalMutation{blocked}); err != nil {
		t.Fatal(err)
	}
	foreignCtx, releaseForeign, err := replay.beginSession(t.Context(), "foreign", "")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseForeign()
	for _, err := range []error{
		u.proxy.journals.initialize(foreignCtx, replay, workspace, "foreign", "/root", ""),
		u.proxy.journals.bindIdentity(foreignCtx, replay, workspace, "foreign", "", "/root", true),
		u.proxy.journals.bindRun(foreignCtx, replay, workspace, "foreign", journalRun{Directory: u.proxy.orchestration.store.Directory, Workspace: u.session.cwd, Main: "main"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := u.proxy.applyJournal(foreignCtx, workspace, "foreign", "block", []journalMutation{blocked}); err != nil {
		t.Fatal(err)
	}
	u.flushOrchestratedEvents()
	if len(u.orchestrateEvents) != 0 {
		t.Fatal("Main or an unconfirmed root emitted a batch blocker")
	}
	// A failed storage write publishes no semantic event and keeps prior state.
	replay.maxBytes = 1
	if _, err := u.proxy.applyJournal(ctx, workspace, "child", "failed", []journalMutation{blocked}); err == nil {
		t.Fatal("expected persistence failure")
	}
	u.flushOrchestratedEvents()
	j, exists, err := readThreadJournal(replay, workspace, "child")
	if err != nil || !exists || len(j.Items) != 1 || len(u.orchestrateEvents) != 0 {
		t.Fatal("failed mutation published a blocker or changed the journal", j.Items, err)
	}
}
