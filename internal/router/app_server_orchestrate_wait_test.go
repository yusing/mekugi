package router

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"github.com/yusing/mekugi/internal/orchestrate"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

func TestAppServerOrchestrateFinalMessageNotification(t *testing.T) {
	u, request := orchestrateIdentityPendingTurn(t)
	w := u.client.Input.(*appServerTestInput)
	orchestrateTestReply(t, u, request, `{"turn":{"id":"running-turn"}}`)
	for i, answer := range []string{"Batch complete.\nChecks passed.", "", "Unphased final response", "Rejected delivery", ""} {
		turn := fmt.Sprintf("turn-%d", i)
		c := &orchestrateCommand{ctx: t.Context(), main: "main", workspace: u.session.cwd, tool: "wait_agent", callID: turn, reply: make(chan orchestrateResult, 1)}
		if i == 2 {
			u.startOrchestratedChild(c)
			drainOrchestrateWork(t, u)
			if r := <-c.reply; r.err != nil || r.wait.State != "registered" {
				t.Fatal(r)
			}
		}

		orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"turn/started","params":{"threadId":"child","turn":{"id":%q}}}`, turn))
		if i != 1 {
			orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"item/completed","params":{"threadId":"child","turnId":%q,"item":{"id":"commentary","type":"agentMessage","text":"Earlier commentary"}}}`, turn))
			phase := ""
			if i == 0 {
				phase = "final_answer"
			}
			orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"item/completed","params":{"threadId":"child","turnId":%q,"item":{"id":"answer","type":"agentMessage","phase":%q,"text":%q}}}`, turn, phase, answer))
			// Other threads and asynchronous questions cannot replace the answer.
			u.orchestrateMessage(appserver.Message{Method: "item/completed", Params: []byte(fmt.Sprintf(`{"threadId":"native","turnId":%q,"item":{"type":"agentMessage","text":"Native answer"}}`, turn))})
			orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"item/completed","params":{"threadId":"child","turnId":%q,"item":{"type":"agentMessage","delivery":"async","text":"Question"}}}`, turn))
		}
		u.orchestrateMessage(appserver.Message{Method: "turn/completed", Params: []byte(fmt.Sprintf(`{"threadId":"child","turn":{"id":%q,"status":"completed"}}`, turn))})
		drainOrchestrateWork(t, u)
		var rpc struct {
			Params struct {
				Items []struct {
					Role    string
					Content []struct{ Text string }
				}
			}
		}
		if err := json.Unmarshal(w.Bytes(), &rpc, json.MatchCaseInsensitiveNames(true)); err != nil || len(rpc.Params.Items) != 1 {
			t.Fatal("missing automatic notification", w.String(), err)
		}
		item := rpc.Params.Items[0]
		if item.Role != "assistant" || len(item.Content) != 1 || item.Content[0].Text != "Message Type: FINAL_ANSWER\nTask name: /root\nSender: /root/batch\nPayload:\n"+answer {
			t.Fatal("wrong notification or premature wait result", item)
		}
		inject := btwTestRequest(t, w, "thread/inject_items", "main")
		if i == 3 {
			orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"message":"rejected"}}`, inject.ID))
		} else {
			orchestrateTestReply(t, u, inject, `{}`)
		}
		if i == 2 {
			if !strings.Contains(w.String(), "WAIT_RESULT") {
				t.Fatal("acknowledged final answer did not settle wait", w.String())
			}
			orchestrateTestReply(t, u, btwTestRequest(t, w, "thread/inject_items", "main"), `{}`)
		}
		u.startOrchestratedChild(c)
		drainOrchestrateWork(t, u)
		if i == 2 {
			// Repeating a delivered call reports delivery without retaining the event.
			if r := <-c.reply; r.err != nil || r.wait == nil || r.wait.State != "delivered" || len(r.wait.Event) != 0 {
				t.Fatalf("repeated delivered wait: %+v", r)
			}
		} else if event := waitTestEvent(t, <-c.reply); i == 3 {
			if event.Status != "delivery_failed" || event.Message != "rejected" || u.orchestrateThreads["child"].batch.Launch.HostStatus != "completed" {
				t.Fatal("delivery failure lost or changed child outcome", event)
			}
		} else if event.Message != "" || event.Kind != "done" || event.Turn != turn {
			t.Fatalf("wait lost completion or repeated the answer: %+v", event)
		}
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"child","turn":{"id":%q,"status":"completed"}}}`, turn))
		if w.Len() != 0 || len(u.orchestrateEvents) != 0 {
			t.Fatal("completion replay repeated delivery or started a turn", w.String())
		}
	}
	batches, err := u.proxy.orchestration.store.List(t.Context(), u.session.cwd, "main")
	if err != nil || batches[0].Error != "" {
		t.Fatal("answer was retained as a launch error", batches, err)
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
	count := 0
	wait := func(ctx context.Context) *orchestrateCommand {
		count++
		c := &orchestrateCommand{ctx: ctx, callID: fmt.Sprint("journal-wait-", count), main: "main", workspace: u.session.cwd, tool: "wait_agent", reply: make(chan orchestrateResult, 1)}
		u.startOrchestratedChild(c)
		drainOrchestrateWork(t, u)
		return c
	}
	apply := func(thread, receipt string, mutations ...journalMutation) {
		t.Helper()
		if _, err := u.proxy.applyJournal(ctx, workspace, thread, receipt, mutations); err != nil {
			t.Fatal(err)
		}
	}
	blocked := journalMutation{Op: "add", Kind: "task", Title: new("Build batch"), State: new("blocked"), Reason: new("Missing input")}
	apply("child", "block", blocked)
	u.flushOrchestratedEvents()
	drainOrchestrateWork(t, u)
	drainOrchestrateWork(t, u)
	c := wait(t.Context())
	if event := waitTestEvent(t, <-c.reply); event.Kind != "blocked" || event.Task != "batch" || event.Thread != "child" || event.Path != "/1" || event.Message != "Missing input" || u.orchestrateThreads["child"].turn != "child-turn" {
		t.Fatal("unbound child blocker changed host lifecycle or lost scope", event)
	}
	// Reopening journals and replaying receipts do not repeat wait events.
	u.proxy.journals = newJournalStore()
	apply("child", "block", blocked)
	apply("child", "edit", journalMutation{Op: "set", P: "/1", Reason: new("Updated reason")})
	u.flushOrchestratedEvents()
	drainOrchestrateWork(t, u)
	if len(u.orchestrateEvents) != 0 {
		t.Fatal("receipt replay or reason edit emitted another blocker")
	}
	// A new blocked state after reopening is another observation.
	apply("child", "work", journalMutation{Op: "set", P: "/1", State: new("working")})
	apply("child", "reblock", journalMutation{Op: "set", P: "/1", State: new("blocked"), Reason: new("New input")})
	c = wait(t.Context())
	if event := waitTestEvent(t, <-c.reply); event.Message != "New input" {
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
	if event := waitTestEvent(t, <-c.reply); event.Thread != "worker" || event.Task != "batch" {
		t.Fatal("native blocker was not scoped to its batch", event)
	}
	c = wait(t.Context())
	if event := waitTestEvent(t, <-c.reply); event.Kind != "question" {
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
	drainOrchestrateWork(t, u)
	if len(u.orchestrateEvents) != 0 {
		t.Fatal("Main or an unconfirmed root emitted a batch blocker")
	}
	// A failed storage write publishes no semantic event and keeps prior state.
	replay.maxBytes = 1
	if _, err := u.proxy.applyJournal(ctx, workspace, "child", "failed", []journalMutation{blocked}); err == nil {
		t.Fatal("expected persistence failure")
	}
	u.flushOrchestratedEvents()
	drainOrchestrateWork(t, u)
	j, exists, err := readThreadJournal(replay, workspace, "child")
	if err != nil || !exists || len(j.Items) != 1 || len(u.orchestrateEvents) != 0 {
		t.Fatal("failed mutation published a blocker or changed the journal", j.Items, err)
	}
}

func waitTestEvent(t *testing.T, r orchestrateResult) *orchestrateEvent {
	t.Helper()
	if r.err != nil || r.wait == nil {
		t.Fatalf("wait: %+v", r)
	}
	var e orchestrateEvent
	if err := json.Unmarshal(r.wait.Event, &e); err != nil {
		t.Fatal(r.wait, err)
	}
	return &e
}

func TestAppServerOrchestrateLogicalWaitHostFlow(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
	wire := u.client.Input.(*appServerTestInput)
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	call := func(ctx context.Context, id, cancel string, timeout int) orchestrateResult {
		t.Helper()
		c := &orchestrateCommand{ctx: ctx, workspace: u.session.cwd, main: "main", tool: "wait_agent", callID: id, waitCancel: cancel, waitTimeout: timeout, reply: make(chan orchestrateResult, 1)}
		u.startOrchestratedChild(c)
		drainOrchestrateWork(t, u)
		return <-c.reply
	}
	ctx, cancel := context.WithCancel(t.Context())
	r := call(ctx, "wait", "", 0)
	if r.err != nil || r.wait.State != "registered" || wire.Len() != 0 {
		t.Fatal(r, wire.String())
	}
	cancel() // MCP disconnection cannot cancel a retained subscription.
	if r := call(t.Context(), "wait", "", 0); r.err != nil || r.wait.State != "registered" {
		t.Fatal(r)
	}
	child := u.orchestrateThreads["child"]
	u.orchestrateMessage(appserver.Message{ID: []byte(`42`), Method: "item/tool/requestUserInput", Params: []byte(`{"threadId":"child","turnId":"child-turn"}`)})
	drainOrchestrateWork(t, u)
	if !strings.Contains(wire.String(), "WAIT_RESULT") || strings.Contains(wire.String(), "FINAL_ANSWER") || strings.Contains(wire.String(), "turn/start") {
		t.Fatal(wire.String())
	}
	inject := btwTestRequest(t, wire, "thread/inject_items", "main")
	// Lost acknowledgement and restart retain the identity without another effect.
	u.proxy.orchestration.store = &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
	u.advanceOrchestratedWaits(u.session.cwd, "main", nil)
	drainOrchestrateWork(t, u)
	if wire.Len() != 0 {
		t.Fatal("recovery repeated injection", wire.String())
	}
	r = call(t.Context(), "wait", "", 0)
	if r.wait.State != "dispatching" || r.wait.EventID == "" {
		t.Fatal(r.wait)
	}
	orchestrateTestReply(t, u, inject, `{}`)
	if r = call(t.Context(), "wait", "", 0); r.wait.State != "delivered" {
		t.Fatal(r.wait)
	}
	if r = call(t.Context(), "finite", "", 5); r.wait.State != "registered" {
		t.Fatal(r.wait)
	}
	now = now.Add(5 * time.Millisecond)
	if err := u.tickOrchestratedViews(u); err != nil {
		t.Fatal(err)
	}
	drainOrchestrateWork(t, u)
	if !strings.Contains(wire.String(), `\"timed_out\":true`) {
		t.Fatal("missing asynchronous expiry", wire.String())
	}
	orchestrateTestReply(t, u, btwTestRequest(t, wire, "thread/inject_items", "main"), `{}`)
	if r = call(t.Context(), "cancel-me", "", 0); r.wait.State != "registered" {
		t.Fatal(r.wait)
	}
	if r = call(t.Context(), "cancel-call", "cancel-me", 0); r.wait.State != "canceled" {
		t.Fatal(r.wait)
	}
	u.publishOrchestratedEvent(child, orchestrateEvent{Task: "batch", Thread: "child", Kind: "approval", Item: "approval"})
	drainOrchestrateWork(t, u)
	if wire.Len() != 0 {
		t.Fatal("canceled wait injected", wire.String())
	}
	if e := waitTestEvent(t, call(t.Context(), "observe", "", 0)); e.Kind != "approval" {
		t.Fatal(e)
	}
}

func TestAppServerOrchestrateWaitRecoveryDeadline(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"running"}}`)
	store, workspace := u.proxy.orchestration.store, u.session.cwd
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	if _, err := store.RegisterWait(t.Context(), workspace, "main", orchestrate.Wait{ID: "resume", TimeoutMS: 1}, now); err != nil {
		t.Fatal(err)
	}
	restored, wire := newAppServerTestUI()
	restored.ctx = t.Context()
	restored.session.start("main", workspace)
	restored.thread = "main"
	restored.ensureShell()
	t.Cleanup(func() {
		restored.closeOrchestrateStorage()
		restored.closeOrchestratedViews()
		restored.shell.diff.close()
		restored.shell.diffScreen.Close()
	})
	restored.clock = func() time.Time { return now.Add(time.Second) }
	restored.proxy = &mekugiProxy{orchestration: &orchestrateRuntime{store: &orchestrate.Store{Directory: store.Directory}}}
	restored.recoverOrchestratedRun()
	drainOrchestrateWork(t, restored)
	if strings.Contains(wire.String(), "thread/resume") || strings.Contains(wire.String(), "turn/start") || !strings.Contains(wire.String(), "WAIT_RESULT") {
		t.Fatal(wire.String())
	}
	orchestrateTestReply(t, restored, btwTestRequest(t, wire, "thread/inject_items", "main"), `{}`)
	if w, err := store.RegisterWait(t.Context(), workspace, "main", orchestrate.Wait{ID: "resume", TimeoutMS: 1}, now); err != nil || w.State != "delivered" || !w.TimedOut {
		t.Fatal(w, err)
	}
}

func TestAppServerOrchestrateWaitFailuresAndDescendants(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"running"}}`)
	observe := func(id string) *orchestrateEvent {
		t.Helper()
		c := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", tool: "wait_agent", callID: id, reply: make(chan orchestrateResult, 1)}
		u.startOrchestratedChild(c)
		drainOrchestrateWork(t, u)
		return waitTestEvent(t, <-c.reply)
	}
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"running","status":"failed","error":{"message":"host failure"}}}}`)
	if e := observe("failed"); e.Kind != "failure" || e.Status != "failed" || e.Message != "host failure" {
		t.Fatal(e)
	}
	u.session.registerThread(appServerThreadInfo{ID: "native", ParentThreadID: "child"})
	for _, kind := range []string{"question", "approval"} {
		method := "item/tool/requestUserInput"
		if kind == "approval" {
			method = "item/commandExecution/requestApproval"
		}
		u.orchestrateMessage(appserver.Message{ID: []byte(`7`), Method: method, Params: []byte(`{"threadId":"native","turnId":"native-turn"}`)})
		if e := observe(kind); e.Kind != kind || e.Thread != "native" || e.Task != "batch" || string(e.Request) != "7" {
			t.Fatal(e)
		}
	}
	store := u.proxy.orchestration.store
	directory := store.Directory
	store.Directory = "invalid-relative-storage"
	u.orchestrateMessage(appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"child","turn":{"id":"unsaved","status":"completed"}}`)})
	drainOrchestrateWork(t, u)
	if u.orchestrateStorageErr == nil || len(u.orchestrateEvents) == 0 {
		t.Fatal("failed persistence lost its event")
	}
	store.Directory = directory
	u.clock = func() time.Time { return time.Now().Add(2 * time.Second) }
	u.flushOrchestratedEvents()
	drainOrchestrateWork(t, u)
	if e := observe("storage-failed"); e.Kind != "failure" || e.Status != "storage_failed" {
		t.Fatal(e)
	}
}
