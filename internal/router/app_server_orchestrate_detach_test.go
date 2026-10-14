package router

import (
	"fmt"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func idleOrchestration(t *testing.T) (*appServerUI, *appServerTestInput) {
	t.Helper()
	u, launch := orchestrateIdentityPendingTurn(t)
	u.proxy.activity = newSubagentActivity()
	w := u.client.Input.(*appServerTestInput)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"finished"}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"finished","status":"completed"}}}`)
	orchestrateTestReply(t, u, btwTestRequest(t, w, "thread/inject_items", "main"), `{}`)
	retainOrchestratedTestPermissions(t, u, u.orchestrateThreads["child"].batch)
	return u, w
}

func TestAppServerOrchestrateCoordinatorReplacement(t *testing.T) {
	for _, command := range []string{"/clear", "/resume other"} {
		t.Run(command, func(t *testing.T) {
			u, w := idleOrchestration(t)
			b := u.orchestrateThreads["child"].batch
			n := u.navigation
			appServerTestKeys(t, u, command+"\r")
			var request btwTestRPC
			if command == "/clear" {
				request = btwTestRequest(t, w, "thread/start", "")
			} else {
				request.ID = resumeTestPrepared(t, u, w).ID
			}
			// No detachment until the host confirms its replacement.
			if u.navigation != n || len(u.orchestrateThreads) != 1 || n.closed {
				t.Fatal("replacement intent detached the active run")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"replacement rejected"}}`, request.ID))
			if u.navigation != n || len(u.orchestrateThreads) != 1 || n.closed {
				t.Fatal("failed replacement detached the active run")
			}
			appServerTestKeys(t, u, command+"\r")
			if command == "/clear" {
				request = btwTestRequest(t, w, "thread/start", "")
			} else {
				request.ID = resumeTestPrepared(t, u, w).ID
			}
			btwTestReply(t, u, request, fmt.Sprintf(`{"thread":{"id":"other","cwd":%q},"model":"model"}`, u.session.cwd))
			resumeTestSettle(t, u, w)
			drainOrchestrateWork(t, u)
			if !n.closed || u.navigation != nil || len(u.orchestrateThreads) != 0 || !u.orchestrateWaitDeadline.IsZero() || len(u.orchestrateEvents) != 0 {
				t.Fatal("confirmed replacement retained live orchestration")
			}
			appServerTestKeys(t, u, "/resume main\r")
			back := resumeTestPrepared(t, u, w)
			btwTestReply(t, u, btwTestRPC{ID: back.ID}, fmt.Sprintf(`{"thread":{"id":"main","cwd":%q},"model":"model"}`, u.session.cwd))
			resumeTestSettle(t, u, w)
			follow := func(id string) *orchestrateCommand {
				c := &orchestrateCommand{ctx: t.Context(), main: "main", workspace: u.session.cwd, tool: "followup_task", target: "batch", callID: id, input: orchestrateSpawnInput{Message: "Continue"}, reply: make(chan orchestrateResult, 1)}
				u.startOrchestratedChild(c)
				return c
			}
			// Before discovery there is no live child, including when returning to
			// the same coordinator identity within the same UI instance.
			if r := <-follow("before-discovery").reply; r.err == nil || w.Len() != 0 {
				t.Fatal("follow-up bypassed discovery", r.err, w.String())
			}
			drainOrchestrateWork(t, u)
			orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"late"}}}`)
			orchestrateTestMessage(t, u, `{"method":"thread/started","params":{"thread":{"id":"late-native","source":{"subAgent":{"thread_spawn":{"parent_thread_id":"child","agent_path":"/root/worker"}}}}}}`)
			orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"late-native","turn":{"id":"late-native-turn"}}}`)
			if u.session.paths["late-native"] != "" {
				t.Fatal("source-encoded retired descendant entered the replacement roster")
			}
			if len(u.orchestrateThreads) != 0 || len(u.navigation.pending) != 0 || w.Len() != 0 {
				t.Fatal("late host event revived a retired batch")
			}
			c := follow("after-discovery")
			drainOrchestrateWork(t, u)
			before := w.String()
			orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"finished","status":"completed"}}}`)
			if w.String() != before {
				t.Fatal("retired lifecycle traffic dispatched before batch admission", w.String())
			}
			if command == "/resume other" {
				read := btwTestRequest(t, w, "thread/read", "child")
				btwTestReply(t, u, read, `{"thread":{"id":"child","cwd":"/wrong"}}`)
				if r := <-c.reply; r.err == nil || u.retiredThreads["child"] == "" || w.Len() != 0 {
					t.Fatal("failed admission released retirement", r.err, w.String())
				}
				c = follow("retry-admission")
				drainOrchestrateWork(t, u)
			}
			resumeBatchResponses(t, u, w, b, false)
			drainOrchestrateWork(t, u)
			orchestrateTestReply(t, u, btwTestRequest(t, w, "turn/start", "child"), `{"turn":{"id":"next"}}`)
			if r := <-c.reply; r.err != nil || r.delivery.State != "delivered" {
				t.Fatal("admitted follow-up failed", r.err)
			}
			if v := u.navigation.views["child"]; v.model != "batch-model" || v.reasoningEffort != "high" {
				t.Fatal("follow-up lost the recipient settings")
			}
		})
	}
}

func TestAppServerOrchestrateReplacementRetiresLifecycleCallback(t *testing.T) {
	u, w := idleOrchestration(t)
	appServerTestKeys(t, u, "/clear\r")
	request := btwTestRequest(t, w, "thread/start", "")
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"late","status":"completed"}}}`)
	btwTestReply(t, u, request, fmt.Sprintf(`{"thread":{"id":"other","cwd":%q}}`, u.session.cwd))
	resumeTestSettle(t, u, w)
	drainOrchestrateWork(t, u)
	if w.Len() != 0 || len(u.orchestrateThreads) != 0 || !u.orchestrateWaitDeadline.IsZero() {
		t.Fatal("retired lifecycle callback dispatched to the old coordinator", w.String())
	}
}

func TestAppServerOrchestrateReplacementRetiresWaitCallbacks(t *testing.T) {
	u, w := idleOrchestration(t)
	store, workspace := u.proxy.orchestration.store, u.session.cwd
	now := time.Now()
	// Consume the fixture's already completed batch event first.
	if _, err := store.RegisterWait(t.Context(), workspace, "main", orchestrate.Wait{ID: "previous"}, now); err != nil {
		t.Fatal(err)
	}
	wait := orchestrate.Wait{ID: "detached"}
	if _, err := store.RegisterWait(t.Context(), workspace, "main", wait, now); err != nil {
		t.Fatal(err)
	}
	appServerTestKeys(t, u, "/clear\r")
	request := btwTestRequest(t, w, "thread/start", "")
	u.advanceOrchestratedWaits(workspace, "main", []orchestrateEvent{{Task: "batch", Kind: "done"}})
	btwTestReply(t, u, request, fmt.Sprintf(`{"thread":{"id":"other","cwd":%q}}`, workspace))
	resumeTestSettle(t, u, w)
	drainOrchestrateWork(t, u)
	// A late journal publication belongs to the detached run as well.
	u.proxy.orchestration.journalEvents = []orchestrateEvent{{main: "main", workspace: workspace, Task: "batch", Kind: "blocked"}}
	u.flushOrchestratedEvents()
	if w.Len() != 0 || !u.orchestrateWaitDeadline.IsZero() || len(u.orchestrateEvents) != 0 || len(u.orchestrateJobs) != 0 {
		t.Fatal("retired callback revived wait delivery", w.String())
	}
	if retained, err := store.RegisterWait(t.Context(), workspace, "main", wait, now); err != nil || retained.State != "ready" {
		t.Fatal("replacement lost the durable wait result", retained, err)
	}
}
