package sessioncontrol

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestHostCallSharesLedgerAndSettlesOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &requestTransport{id: "host-id"}
		requests := NewRequests[string](transport)
		result := make(chan hostCallResult, 1)
		go func() {
			value, err := requests.call(t.Context(), "child", "thread/start", jsontext.Value(`{"cwd":"/child"}`))
			result <- hostCallResult{value, err}
		}()
		call := <-requests.Calls()
		if requests.Pending() || len(transport.sends) != 0 {
			t.Fatal("handler dispatched outside the event loop")
		}
		requests.Dispatch(call)
		if pending, ok := requests.Lookup("host-id"); !ok || pending.Scope != "child" || pending.Method != "thread/start" {
			t.Fatalf("operation missing from existing ledger: %+v", pending)
		}
		message := appserver.Message{ID: jsontext.Value(`host-id`), Result: jsontext.Value(`{"thread":{"id":"child"}}`)}
		if !requests.CompleteCall(message) || requests.CompleteCall(message) || requests.Pending() {
			t.Fatal("reply did not settle exactly once")
		}
		got := <-result
		if got.err != nil || string(got.value) != string(message.Result) || len(transport.sends) != 1 {
			t.Fatalf("result=%s err=%v sends=%d", got.value, got.err, len(transport.sends))
		}
	})
}

func TestHostCallCancellationAndUncertainDispatch(t *testing.T) {
	for _, phase := range []string{"before_handoff", "before_dispatch", "after_dispatch", "retired", "closed", "rejected", "send_failure"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				transport := &requestTransport{id: "request"}
				requests := NewRequests[struct{}](transport)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if phase == "before_handoff" {
					cancel()
				}
				result := make(chan error, 1)
				go func() {
					_, err := requests.call(ctx, "work", "turn/start", jsontext.Value(`{"input":[]}`))
					result <- err
				}()
				if phase != "before_handoff" {
					call := <-requests.Calls()
					if phase == "before_dispatch" {
						cancel()
					}
					if phase == "send_failure" {
						transport.err = errors.New("partial write")
					}
					requests.Dispatch(call)
					switch phase {
					case "after_dispatch":
						cancel()
					case "retired":
						requests.Retire("work")
					case "closed":
						requests.Close()
					case "rejected":
						requests.CompleteCall(appserver.Message{ID: jsontext.Value(`request`), Error: &appserver.Error{Code: -32600, Message: "thread not found"}})
					}
				}
				err := <-result
				failure, ok := errors.AsType[*binding.DispatchError](err)
				want := binding.Uncertain
				if phase == "before_handoff" || phase == "before_dispatch" {
					want = binding.NotDispatched
					if len(transport.sends) != 0 {
						t.Fatal("canceled operation executed")
					}
				} else if phase == "rejected" {
					want = binding.HostRejected
				}
				if !ok || failure.Outcome != want {
					t.Fatalf("outcome=%v want %s", err, want)
				}
				if phase == "after_dispatch" {
					if !requests.Pending() || !requests.CompleteCall(appserver.Message{ID: jsontext.Value(`request`), Result: jsontext.Value(`{}`)}) {
						t.Fatal("cancellation discarded the dispatched operation's ledger identity")
					}
				}
			})
		})
	}
}

func TestClientFailedDispatchKeepsInputAdmission(t *testing.T) {
	for _, method := range []string{"turn/start", "thread/compact/start"} {
		t.Run(method, func(t *testing.T) {
			transport := &requestTransport{err: errors.New("partial write")}
			client := &Client[struct{}]{Requests: NewRequests[struct{}](transport)}
			client.Thread = "main"
			call := &HostCall{ctx: t.Context(), scope: "main", method: method, result: make(chan hostCallResult, 1)}
			client.Dispatch(call)
			failure, ok := errors.AsType[*binding.DispatchError]((<-call.result).err)
			if !ok || failure.Outcome != binding.Uncertain || call.phase.Load() != callDispatched {
				t.Fatalf("failed send lost uncertainty: %v", failure)
			}
			if client.AwaitingTurn || client.Starting() || client.Compaction.Pending() || client.Requests.Pending() {
				t.Fatal("failed send blocked new input")
			}
		})
	}
}
