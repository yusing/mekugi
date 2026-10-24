package sessioncontrol

import (
	"encoding/json/jsontext"
	"errors"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestHumanInputCallbackSharesRequestLedger(t *testing.T) {
	for _, mode := range []string{"start", "steer", "rejected", "malformed", "retired", "transport"} {
		t.Run(mode, func(t *testing.T) {
			transport := &requestTransport{id: "request"}
			if mode == "transport" {
				transport.err = errors.New("write failed")
			}
			requests := NewRequests[string](transport)
			calls, turn := 0, ""
			var outcome error
			input := InputRequest{Thread: "child", ID: "human", Input: []map[string]any{{"type": "text", "text": "ordinary human input"}}, Settled: func(id string, err error) { calls++; turn, outcome = id, err }}
			if mode == "steer" {
				input.Turn = "active"
			}
			_, err := requests.SendInput(input, "original UI owner")
			if (err != nil) != (mode == "transport") {
				t.Fatal(err)
			}
			params := transport.sends[0].params.(map[string]any)
			if params["threadId"] != "child" || params["clientUserMessageId"] != "human" || params["toolOutput"] != nil {
				t.Fatal("human input lost its native identity or role")
			}
			response := appserver.Message{ID: jsontext.Value("request"), Result: jsontext.Value(`{"turn":{"id":"started"}}`)}
			switch mode {
			case "retired":
				requests.Retire("main")
			case "rejected":
				response.Error = &appserver.Error{Message: "rejected"}
			case "malformed":
				response.Result = jsontext.Value(`{}`)
			}
			for range 2 {
				if requests.CompleteCall(response) {
					t.Fatal("callback consumed ordinary UI response routing")
				}
			}
			if mode != "retired" && mode != "transport" {
				pending, ok := requests.Take("request")
				if !ok || pending.Owner != "original UI owner" {
					t.Fatal("ordinary UI lost correlated response")
				}
			}
			requests.Retire("main")
			if calls != 1 {
				t.Fatalf("settlements = %d", calls)
			}
			if mode == "start" || mode == "steer" {
				want := "started"
				if mode == "steer" {
					want = "active"
				}
				if outcome != nil || turn != want {
					t.Fatalf("turn=%s outcome=%v", turn, outcome)
				}
			} else {
				failure, ok := errors.AsType[*binding.DispatchError](outcome)
				want := binding.Uncertain
				if mode == "rejected" {
					want = binding.HostRejected
				}
				if !ok || failure.Outcome != want {
					t.Fatalf("outcome = %v", outcome)
				}
			}
		})
	}
}
