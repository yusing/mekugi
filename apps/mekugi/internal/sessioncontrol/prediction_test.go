package sessioncontrol

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestPredictionHostCapabilityAndEligibility(t *testing.T) {
	for version, want := range map[string]bool{"": false, "0.161.0": false, "0.162.0-alpha.19": false, "0.162.0-alpha.20": true, "0.162.0": true, "0.163.0": true} {
		if PredictionForkSupported(version) != want {
			t.Fatalf("capability %q", version)
		}
	}
	for _, gate := range []string{"provider", "third-party-model", "unsupported-model", "plan", "source-turn"} {
		t.Run(gate, func(t *testing.T) {
			transport := &requestTransport{id: "request"}
			requests := NewRequests[struct{}](transport)
			client := &Client[struct{}]{Requests: requests}
			p := &Prediction{SourceThread: "main", SourceTurn: "source", UnsupportedModels: []string{"blocked"}}
			result := map[string]any{"thread": map[string]any{"id": "main", "turns": []map[string]string{{"id": "source", "status": "completed"}}}, "modelProvider": "openai", "model": "gpt-6-astra"}
			switch gate {
			case "provider":
				result["modelProvider"] = "other"
			case "third-party-model":
				result["model"] = "grok:grok-4"
			case "unsupported-model":
				result["model"] = "blocked"
			case "plan":
				result["collaborationMode"] = map[string]string{"mode": "plan"}
			case "source-turn":
				p.SourceTurn = "stale"
			}
			if err := requests.StartPrediction(p); err != nil {
				t.Fatal(err)
			}
			request, _ := requests.Take("request")
			body, _ := json.Marshal(result)
			if handled, err := client.ObservePrediction(appserver.Message{Result: body}, request, nil); !handled || err != nil {
				t.Fatal(err)
			}
			if !p.Closed || len(transport.sends) != 1 {
				t.Fatal("ineligible source forked")
			}
		})
	}
}

func TestPredictionLifecycleAndLateCompletion(t *testing.T) {
	for _, closeAt := range []string{"", "resume", "fork", "start"} {
		t.Run(closeAt, func(t *testing.T) {
			transport := &requestTransport{id: "request"}
			requests := NewRequests[struct{}](transport)
			client := &Client[struct{}]{Requests: requests}
			observations := new(Observations)
			p := &Prediction{SourceThread: "main", SourceTurn: "source", Prompt: "remote", Trigger: "live"}
			if err := requests.StartPrediction(p); err != nil {
				t.Fatal(err)
			}
			reply := func(body string) {
				t.Helper()
				request, ok := requests.Take("request")
				if !ok {
					t.Fatal("missing ledger entry")
				}
				handled, err := client.ObservePrediction(appserver.Message{Result: jsontext.Value(body)}, request, observations)
				if !handled || err != nil {
					t.Fatalf("reply handled=%v err=%v", handled, err)
				}
			}
			last := func() string { return transport.sends[len(transport.sends)-1].method }
			if closeAt == "resume" {
				_ = requests.ClosePrediction(p)
			}
			reply(`{"thread":{"id":"main","turns":[{"id":"source","status":"completed"}]},"model":"gpt-6-astra","modelProvider":"openai"}`)
			if closeAt == "resume" {
				if last() != "thread/resume" {
					t.Fatal("closed request forked")
				}
				return
			}
			if last() != "thread/fork" {
				t.Fatal("did not fork")
			}
			params := transport.sends[len(transport.sends)-1].params.(map[string]any)
			if len(params) != 5 || params["experimentalPredictionMode"] != true || params["ephemeral"] != true {
				t.Fatal("prediction fork lost host mode or added overrides")
			}
			if closeAt == "fork" {
				_ = requests.ClosePrediction(p)
			}
			reply(`{"thread":{"id":"prediction"}}`)
			if closeAt == "fork" {
				if last() != "thread/unsubscribe" {
					t.Fatal("closed fork sampled")
				}
				reply(`{}`)
				return
			}
			if !observations.IsSideThread("prediction") || last() != "turn/start" {
				t.Fatal("prediction not isolated before sampling")
			}
			if closeAt == "start" {
				_ = requests.ClosePrediction(p)
				reply(`{"turn":{"id":"turn"}}`)
				if last() != "turn/interrupt" {
					t.Fatal("late start not interrupted")
				}
				reply(`{}`)
				if last() != "thread/unsubscribe" {
					t.Fatal("interrupted fork not released")
				}
				return
			}
			// Completion may precede the start acknowledgement.
			for _, m := range []appserver.Message{{Method: "item/completed", Params: []byte(`{"threadId":"prediction","turnId":"turn","item":{"type":"agentMessage","text":"{\"suggestion\":\"Show tests\"}"}}`)}, {Method: "turn/completed", Params: []byte(`{"threadId":"prediction","turn":{"id":"turn","status":"completed"}}`)}} {
				if handled, err := client.ObservePrediction(m, Request[struct{}]{}, observations); !handled || err != nil {
					t.Fatal(err)
				}
			}
			reply(`{"turn":{"id":"turn"}}`)
			if p.Text != "Show tests" || !p.Completed || p.Closed || last() != "thread/unsubscribe" {
				t.Fatalf("lost result/cleanup %+v", p)
			}
			reply(`{}`)
			if observations.IsSideThread("prediction") || requests.Pending() {
				t.Fatal("prediction resources retained")
			}
		})
	}
}

func TestPredictionCompletedResultRejectsLateItems(t *testing.T) {
	transport := &requestTransport{id: "request"}
	requests := NewRequests[struct{}](transport)
	client := &Client[struct{}]{Requests: requests}
	p := &Prediction{Thread: "prediction", Turn: "turn", Text: `{"suggestion":"Show tests"}`}
	requests.predictions = map[string]*Prediction{"prediction": p}
	for _, event := range []appserver.Message{
		{Method: "turn/completed", Params: []byte(`{"threadId":"prediction","turn":{"id":"turn","status":"completed"}}`)},
		{Method: "item/completed", Params: []byte(`{"threadId":"prediction","turnId":"turn","item":{"type":"agentMessage","text":"{\"suggestion\":\"late replacement\"}"}}`)},
		{Method: "item/agentMessage/delta", Params: []byte(`{"threadId":"prediction","turnId":"turn","delta":"late delta"}`)},
	} {
		if handled, err := client.ObservePrediction(event, Request[struct{}]{}, nil); !handled || err != nil {
			t.Fatal(err)
		}
	}
	if p.Text != "Show tests" {
		t.Fatal("completed result changed", p.Text)
	}
}
