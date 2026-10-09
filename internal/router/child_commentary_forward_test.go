package router

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCollaborationCallsPassThroughWithoutCommentary(t *testing.T) {
	for _, namespace := range []string{"collaboration", "native_agents"} {
		for _, name := range []string{"spawn_agent", "followup_task", "send_message", "wait_agent", "interrupt_agent"} {
			t.Run(namespace+"/"+name, func(t *testing.T) {
				transform, proxy, _ := newSubagentCommentaryTestTransform(t, nil)
				call := map[string]any{"type": "function_call", "id": "item", "call_id": "call",
					"namespace": namespace, "name": name, "arguments": `{"message":"opaque-secret","agent_type":"worker"}`}
				original := mustTestJSON(t, call)
				for _, event := range []map[string]any{
					{"type": "response.output_item.added", "item": call},
					{"type": "response.function_call_arguments.delta", "item_id": "item", "delta": "opaque"},
					{"type": "response.function_call_arguments.done", "item_id": "item", "arguments": call["arguments"]},
					{"type": "response.output_item.done", "item": call},
				} {
					payload := mustTestJSON(t, event)
					events, err := transform.TransformSSE(payload)
					if err != nil || len(events) != 1 || !bytes.Equal(events[0], payload) {
						t.Fatalf("collaboration event changed: %s, %v", events, err)
					}
				}
				if err := transform.Finish(true); err != nil {
					t.Fatal(err)
				}
				payload := mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}})
				output, err := transform.TransformJSON(payload)
				if err != nil {
					t.Fatal(err)
				}
				var response struct{ Output []json.RawMessage }
				if err := json.Unmarshal(output, &response); err != nil || len(response.Output) != 1 || !bytes.Equal(response.Output[0], original) {
					t.Fatalf("collaboration JSON changed: %s, %v", output, err)
				}
				request := &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustTestJSON(t, []any{call})}}
				if err := proxy.reconcileInputPrefix(request, transform.historySessionID); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(request.fields["input"], mustTestJSON(t, []any{call})) {
					t.Fatal("collaboration replay changed")
				}
			})
		}
	}
}
