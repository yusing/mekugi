package sessioncontrol

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestNativeCompletionRetainedRequestRecovery(t *testing.T) {
	for _, outcome := range []control.Status{control.Completed, control.Interrupted, control.Failed, ""} {
		name := string(outcome)
		if name == "" {
			name = "legacy-unknown"
		}
		t.Run(name, func(t *testing.T) {
			transport := &requestTransport{id: "native"}
			requests := NewRequests[struct{}](transport)
			input := control.NativeInput{
				Thread: "parent", ID: "child:completion", Kind: "FINAL_ANSWER",
				Sender: "/root/child", Recipient: "/root", Outcome: outcome,
				Message: "Status: failed\nStatus: interrupted\nOutcome: failed\nPayload:\nverbatim final\n",
			}
			if outcome != "" {
				input.SourceGeneration, input.ParentTurn = 2, "child-turn"
			}
			done := make(chan error, 1)
			go func() {
				_, err := requests.Input(t.Context(), input)
				done <- err
			}()
			requests.Dispatch(<-requests.Calls())
			if len(transport.sends) != 1 || transport.sends[0].method != "thread/inject_items" {
				t.Fatalf("completion did not use retained-only injection: %+v", transport.sends)
			}
			var params struct {
				Thread string           `json:"threadId"`
				Items  []jsontext.Value `json:"items"`
			}
			if err := json.Unmarshal(transport.sends[0].params.(jsontext.Value), &params); err != nil {
				t.Fatal(err)
			}
			if params.Thread != input.Thread || len(params.Items) != 1 {
				t.Fatalf("retained injection = %+v", params)
			}
			requests.CompleteCall(appserver.Message{ID: jsontext.Value(`native`), Result: jsontext.Value(`{}`)})
			if err := <-done; err != nil {
				t.Fatal(err)
			}

			// Retain the actual injected item, without reconstructing its envelope.
			record, err := json.Marshal(map[string]any{"type": "response_item", "payload": params.Items[0]})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			data := append([]byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"parent\"}}\n"), record...)
			if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			if err := ConfirmNativeInput(t.Context(), path, input); err != nil {
				t.Fatalf("produced completion did not confirm from retained history: %v", err)
			}
		})
	}
}
