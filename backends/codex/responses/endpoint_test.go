package responses

import (
	"encoding/json/jsontext"
	"io"
	"strings"
	"testing/synctest"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	json "encoding/json/v2"
	"testing"
)

func TestOpenCodeEndpointSettingsAndParallelHistory(t *testing.T) {
	const arguments = `{"n":9007199254740993, "text":"<>&"}`
	for _, format := range []string{"chat", "responses", "anthropic"} {
		t.Run(format, func(t *testing.T) {
			request := map[string]any{
				"model":        "opencode-go:test",
				"instructions": "system",
				"tools": []any{map[string]any{"type": "namespace", "name": "ns", "tools": []any{
					map[string]any{"type": "function", "name": "run", "strict": true, "parameters": map[string]string{"type": "object"}},
				}}},
				"tool_choice":         map[string]string{"type": "function", "namespace": "ns", "name": "run"},
				"parallel_tool_calls": false,
				"reasoning":           map[string]string{"effort": "high"},
				"text":                map[string]any{"format": map[string]any{"type": "json_schema", "name": "answer", "strict": true, "schema": map[string]string{"type": "object"}}},
				"input": []any{
					map[string]any{"type": "function_call", "namespace": "ns", "name": "run", "call_id": "c1", "arguments": arguments},
					map[string]any{"type": "function_call", "namespace": "ns", "name": "run", "call_id": "c2", "arguments": "{}"},
					map[string]any{"type": "function_call_output", "call_id": "c1", "output": "one"},
					map[string]any{"type": "function_call_output", "call_id": "c2", "output": "two"},
				},
			}
			if format != "chat" {
				request["max_output_tokens"] = 1234
			}
			tr, err := testTranslateFormat(mustTestJSON(t, request), format)
			if err != nil {
				t.Fatal(err)
			}
			wire := responses.MustMarshalJSON(tr.body)
			var body map[string]jsontext.Value
			if err := json.Unmarshal(wire, &body); err != nil {
				t.Fatal(err)
			}
			alias := providerToolName("ns", "run")
			var expectedChoice any
			switch format {
			case "chat":
				expectedChoice = map[string]any{"type": "function", "function": map[string]string{"name": alias}}
				if string(body["parallel_tool_calls"]) != "false" || string(body["reasoning_effort"]) != `"high"` {
					t.Fatalf("Chat settings lost: %s", wire)
				}
				var messages []struct {
					Role  string `json:"role"`
					Calls []struct {
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				}
				if json.Unmarshal(body["messages"], &messages) != nil || len(messages) != 4 ||
					len(messages[1].Calls) != 2 || messages[1].Calls[0].Function.Arguments != arguments ||
					messages[1].Calls[0].Function.Name != alias {
					t.Fatalf("Chat parallel history changed: %s", wire)
				}
			case "responses":
				expectedChoice = map[string]string{"type": "function", "name": alias}
				var input []map[string]jsontext.Value
				if json.Unmarshal(body["input"], &input) != nil || len(input) != 5 ||
					string(input[1]["arguments"]) != string(responses.MustMarshalJSON(arguments)) ||
					string(input[1]["name"]) != string(responses.MustMarshalJSON(alias)) ||
					string(input[3]["type"]) != `"function_call_output"` {
					t.Fatalf("Responses history changed: %s", wire)
				}
				if string(body["max_output_tokens"]) != "1234" || string(body["parallel_tool_calls"]) != "false" ||
					!jsonEquivalent(body["reasoning"], []byte(`{"effort":"high"}`)) {
					t.Fatalf("Responses settings lost: %s", wire)
				}
			case "anthropic":
				expectedChoice = map[string]any{"type": "tool", "name": alias, "disable_parallel_tool_use": true}
				var messages []struct {
					Role    string                      `json:"role"`
					Content []map[string]jsontext.Value `json:"content"`
				}
				if json.Unmarshal(body["messages"], &messages) != nil || len(messages) != 2 ||
					messages[0].Role != "assistant" || len(messages[0].Content) != 2 ||
					messages[1].Role != "user" || len(messages[1].Content) != 2 ||
					!jsonEquivalent(messages[0].Content[0]["input"], []byte(arguments)) {
					t.Fatalf("Messages parallel history changed: %s", wire)
				}
				if string(body["max_tokens"]) != "1234" || len(body["system"]) == 0 {
					t.Fatalf("Messages settings lost: %s", wire)
				}
			}
			if !jsonEquivalent(body["tool_choice"], responses.MustMarshalJSON(expectedChoice)) {
				t.Fatalf("tool choice changed: %s", wire)
			}
		})
	}
}

func TestOpenCodeUsagePreservesSignedBounds(t *testing.T) {
	for index := range 5 {
		counts := []uint64{1, 2, 3, 4, 5}
		counts[index] = uint64(1) << 63
		if _, err := endpointUsage(testPolicy().Diagnostics, counts[0], counts[1], counts[2], counts[3], counts[4]); err == nil {
			t.Fatalf("overflow accepted at count %d", index)
		}
	}
}

func TestEndpointHeartbeatProgress(t *testing.T) {
	for _, format := range []string{"chat", "responses", "anthropic"} {
		for _, heartbeat := range []string{": private keepalive\n\n", "data: {\"type\":\"ping\"}\n\n", "data:\n\n"} {
			if format != "anthropic" && strings.Contains(heartbeat, "ping") {
				continue
			}
			t.Run(format+"/"+strings.TrimSpace(heartbeat), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					request, err := testTranslateFormat([]byte(`{"input":[],"tools":[]}`), format)
					if err != nil {
						t.Fatal(err)
					}
					reader, upstream := io.Pipe()
					defer upstream.Close()
					events := []map[string]any{}
					done := make(chan error, 1)
					go func() {
						_, err := request.codec.Read(reader, func(event map[string]any) error {
							events = append(events, event)
							return nil
						}, func([]byte) string { return "provider error" })
						done <- err
					}()
					if format == "anthropic" {
						if _, err := io.WriteString(upstream, "data: {\"type\":\"message_start\",\"message\":{\"model\":\"fixture\"}}\n\n"); err != nil {
							t.Fatal(err)
						}
					}
					synctest.Wait()
					before := len(events)
					if _, err := io.WriteString(upstream, heartbeat); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					if heartbeat == "data:\n\n" {
						if err := <-done; err == nil {
							t.Fatal("empty data event accepted as a heartbeat")
						}
						return
					}
					if len(events) != before+1 || events[before]["type"] != "response.in_progress" || strings.Contains(string(responses.MustMarshalJSON(events[before:])), "private") {
						t.Fatalf("heartbeat lost or leaked: %v", events[before:])
					}
					terminal := map[string]string{
						"chat":      "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
						"responses": "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[]}}\n\n",
						"anthropic": "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n",
					}[format]
					if _, err := io.WriteString(upstream, terminal); err != nil {
						t.Fatal(err)
					}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				})
			})
		}
	}
}
