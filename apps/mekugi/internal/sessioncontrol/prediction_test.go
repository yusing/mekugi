package sessioncontrol

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestPredictionCleanupFailureRetainsCompletedResult(t *testing.T) {
	for _, failure := range []string{"send", "acknowledgement"} {
		t.Run(failure, func(t *testing.T) {
			transport := &requestTransport{id: "unsubscribe"}
			r := NewRequests[struct{}](transport)
			client := &Client[struct{}]{Requests: r}
			p := &Prediction{Thread: "prediction", Completed: true, Text: "Show tests"}
			r.predictions = map[string]*Prediction{p.Thread: p}
			if failure == "send" {
				transport.err = errors.New("write failed")
			}
			if err := r.releasePrediction(p); (err != nil) != (failure == "send") {
				t.Fatal(err)
			}
			if failure == "acknowledgement" {
				request, _ := r.Take("unsubscribe")
				if _, err := client.ObservePrediction(appserver.Message{Error: &appserver.Error{Message: "rejected"}}, request); err != nil {
					t.Fatal(err)
				}
			}
			transport.err = nil
			if err := r.ClosePrediction(p); err != nil || transport.sends[len(transport.sends)-1].method != "thread/unsubscribe" {
				t.Fatal("failed prediction cleanup cannot retry")
			}
			// A close notification can precede the cleanup acknowledgement.
			if _, err := client.ObservePrediction(appserver.Message{Method: "thread/closed", Params: []byte(`{"threadId":"prediction"}`)}, Request[struct{}]{}); err != nil {
				t.Fatal(err)
			}
			request, _ := r.Take("unsubscribe")
			if _, err := client.ObservePrediction(appserver.Message{}, request); err != nil || r.PredictionPending() || p.Text != "Show tests" || !p.Completed {
				t.Fatal("late cleanup acknowledgement lost the result or correlation")
			}
		})
	}
}

func TestPredictionHostCapabilityAndEligibility(t *testing.T) {
	for version, want := range map[string]bool{"": false, "0.161.0": false, "0.162.0-alpha.19": false, "0.162.0-alpha.20": true, "0.162.0": true, "0.163.0": true} {
		if PredictionForkSupported(version) != want {
			t.Fatalf("capability %q", version)
		}
	}
	for _, gate := range []string{"provider", "unsupported-model", "plan", "source-turn"} {
		t.Run(gate, func(t *testing.T) {
			transport := &requestTransport{id: "request"}
			requests := NewRequests[struct{}](transport)
			client := &Client[struct{}]{Requests: requests}
			p := &Prediction{SourceThread: "main", SourceTurn: "source", UnsupportedModels: []string{"blocked"}}
			result := map[string]any{"thread": map[string]any{"id": "main", "turns": []map[string]string{{"id": "source", "status": "completed"}}}, "modelProvider": "openai", "model": "gpt-6-astra"}
			switch gate {
			case "provider":
				result["modelProvider"] = "other"
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
			if handled, err := client.ObservePrediction(appserver.Message{Result: body}, request); !handled || err != nil {
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
				handled, err := client.ObservePrediction(appserver.Message{Result: jsontext.Value(body)}, request)
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
			if !requests.IsSideThread("prediction") || last() != "turn/start" {
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
				if handled, err := client.ObservePrediction(m, Request[struct{}]{}); !handled || err != nil {
					t.Fatal(err)
				}
			}
			reply(`{"turn":{"id":"turn"}}`)
			if p.Text != "Show tests" || !p.Completed || p.Closed || last() != "thread/unsubscribe" {
				t.Fatalf("lost result/cleanup %+v", p)
			}
			reply(`{}`)
			if requests.IsSideThread("prediction") || requests.Pending() {
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
		if handled, err := client.ObservePrediction(event, Request[struct{}]{}); !handled || err != nil {
			t.Fatal(err)
		}
	}
	if p.Text != "Show tests" {
		t.Fatal("completed result changed", p.Text)
	}
}
