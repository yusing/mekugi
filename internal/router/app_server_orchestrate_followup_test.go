package router

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestAppServerOrchestrateMessageCompletionRace(t *testing.T) {
	for _, tc := range []struct {
		name, response, state string
	}{
		{"before-dispatch", "", "queued"},
		{"host-no-active", `"error":{"code":-32600,"message":"no active turn to steer"}`, "queued"},
		{"wrong-code", `"error":{"code":-32603,"message":"no active turn to steer"}`, "rejected"},
		{"ambiguous-text", `"error":{"code":-32600,"message":"failed: no active turn to steer"}`, "rejected"},
		{"uncertain", `"result":{}`, "uncertain"},
		{"transport", "", "uncertain"},
		{"followup", `"error":{"code":-32600,"message":"no active turn to steer"}`, "rejected"},
		{"canceled", "", "canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, launch := orchestrateIdentityPendingTurn(t)
			orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
			w := u.client.Input.(*appServerTestInput)
			command := func(id, tool, message string) *orchestrateCommand {
				return &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", caller: "main", target: "batch", tool: tool, callID: id, input: orchestrateSpawnInput{Message: message}, reply: make(chan orchestrateResult, 1)}
			}
			first := command("first", "send_message", "  original \"quoted\"\nmessage\t")
			if tc.name == "followup" {
				first.tool = "followup_task"
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			first.ctx = ctx
			if tc.name == "transport" {
				u.client.Input = &sessionTitleFailInput{}
			}
			u.startOrchestratedChild(first)
			if tc.name == "transport" {
				drainOrchestrateWork(t, u)
				u.client.Input = w
				orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}}`)
			} else if tc.response == "" {
				// Hold the actual persistence callback, then observe completion
				// before dispatch can write any host request.
				select {
				case complete := <-u.orchestrateCompletions:
					appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}}`)
					if tc.name == "canceled" {
						cancel()
					}
					u.completeOrchestrateWork(complete)
				case <-time.After(5 * time.Second):
					t.Fatal("delivery persistence timeout")
				}
				drainOrchestrateWork(t, u)
			} else {
				drainOrchestrateWork(t, u)
				req := btwTestRequest(t, w, "turn/steer", "child")
				if len(req.Params.Input) != 1 || req.Params.Input[0].Text != first.input.Message {
					t.Fatal("steer changed original input", req)
				}
				// A no-active rejection can precede the completion notification.
				orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,%s}`, req.ID, tc.response))
				if w.Len() != 0 {
					t.Fatal("host response retried input", w.String())
				}
				orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}}`)
			}
			orchestrateTestReply(t, u, btwTestRequest(t, w, "thread/inject_items", "main"), `{}`)
			got := <-first.reply
			if (got.err != nil) != (tc.state != "queued") || got.delivery == nil || got.delivery.State != tc.state || got.delivery.From != "main" || got.delivery.Message != first.input.Message || got.delivery.TurnID != "" {
				t.Fatalf("completion delivery: %+v, %v", got.delivery, got.err)
			}
			second := command("second", "send_message", "second message")
			u.startOrchestratedChild(second)
			drainOrchestrateWork(t, u)
			if got := <-second.reply; got.err != nil || got.delivery.State != "queued" {
				t.Fatal("second message did not queue", got)
			}
			// Recover through a fresh store. Same-call repeats cannot requeue or
			// replay either known-unsent or possibly consumed input.
			u.proxy.orchestration.store = &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
			first.ctx = t.Context()
			u.startOrchestratedChild(first)
			drainOrchestrateWork(t, u)
			if got := <-first.reply; got.err != nil || got.delivery.State != tc.state || w.Len() != 0 {
				t.Fatal("repeat changed retained delivery or woke target", got, w.String())
			}
			for i := range 2 {
				next := command(fmt.Sprint("next-", i), "followup_task", "authorized assignment")
				u.startOrchestratedChild(next)
				drainOrchestrateWork(t, u)
				req := btwTestRequest(t, w, "turn/start", "child")
				want := next.input.Message
				if i == 0 {
					want = second.input.Message + "\n" + want
					if tc.state == "queued" {
						want = first.input.Message + "\n" + want
					}
				}
				if len(req.Params.Input) != 1 || req.Params.Input[0].Text != want {
					t.Fatal("queue reordered, changed or replayed input", req)
				}
				turn := fmt.Sprint("next-turn-", i)
				orchestrateTestReply(t, u, req, fmt.Sprintf(`{"turn":{"id":%q}}`, turn))
				if got := <-next.reply; got.err != nil || got.delivery.State != "delivered" {
					t.Fatal("authorized follow-up failed", got)
				}
				orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"child","turn":{"id":%q,"status":"completed"}}}`, turn))
				orchestrateTestReply(t, u, btwTestRequest(t, w, "thread/inject_items", "main"), `{}`)
			}
		})
	}
}
