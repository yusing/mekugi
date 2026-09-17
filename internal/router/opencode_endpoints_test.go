package router

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func openCodeEvents(events ...any) string {
	var result strings.Builder
	for _, event := range events {
		fmt.Fprintf(&result, "data: %s\n\n", mustMarshalJSON(event))
	}
	return result.String()
}

func openCodeAnthropicFixture() string {
	return openCodeEvents(
		map[string]any{"type": "message_start", "message": map[string]any{"model": "minimax-provider-alias", "usage": map[string]int{"input_tokens": 10, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 2, "output_tokens": 0}}},
		map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking", "thinking": ""}},
		map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "thinking_delta", "thinking": "private thinking"}},
		map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "signature_delta", "signature": "signed-proof"}},
		map[string]any{"type": "content_block_stop", "index": 0},
		map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]string{"type": "text", "text": "checking"}},
		map[string]any{"type": "content_block_stop", "index": 1},
		map[string]any{"type": "content_block_start", "index": 2, "content_block": map[string]any{"type": "tool_use", "id": "call1", "name": "exec", "input": map[string]any{}}},
		map[string]any{"type": "content_block_delta", "index": 2, "delta": map[string]string{"type": "input_json_delta", "partial_json": `{"input":"text(1)\n"}`}},
		map[string]any{"type": "content_block_stop", "index": 2},
		map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 4}},
		map[string]any{"type": "message_stop"},
	)
}

func openCodeResponsesFixture() string {
	items := []any{
		map[string]any{"type": "reasoning", "id": "rs_provider", "summary": []any{}, "encrypted_content": "provider-cipher"},
		map[string]any{"type": "message", "id": "msg_provider", "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": "checking"}}},
		map[string]any{"type": "function_call", "id": "fc_provider", "call_id": "call1", "name": "exec", "arguments": `{"input":"text(1)\n"}`, "status": "completed"},
	}
	return openCodeEvents(
		map[string]any{"type": "response.created", "response": map[string]any{"model": "grok-provider-alias"}},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": items[0]},
		map[string]any{"type": "response.output_text.delta", "output_index": 1, "delta": "checking"},
		map[string]any{"type": "response.output_item.done", "output_index": 1, "item": items[1]},
		map[string]any{"type": "response.output_item.done", "output_index": 2, "item": items[2]},
		map[string]any{"type": "response.completed", "response": map[string]any{"model": "grok-provider-alias", "output": items, "usage": map[string]any{
			"input_tokens": 15, "output_tokens": 4, "input_tokens_details": map[string]int{"cached_tokens": 3}, "output_tokens_details": map[string]int{"reasoning_tokens": 2},
		}}},
	)
}

func TestOpenCodeEndpointRequestsAndReplay(t *testing.T) {
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "go-key"}}).services()[0]
	for _, test := range []struct {
		model, path, fixture, retained string
	}{
		{"minimax-m3", "/messages", openCodeAnthropicFixture(), "signed-proof"},
		{"grok-4.6", "/responses", openCodeResponsesFixture(), "provider-cipher"},
	} {
		t.Run(test.model, func(t *testing.T) {
			for iteration := range 20 {
				stream := iteration != 0
				request := map[string]any{
					"model": service.prefix + ":" + test.model, "stream": stream,
					"input": []any{map[string]any{"role": "user", "content": []any{
						map[string]string{"type": "input_text", "text": "task"},
						map[string]string{"type": "input_image", "image_url": "data:image/png;base64,AA=="},
					}}},
					"tools":     []any{map[string]string{"type": "custom", "name": "exec"}},
					"reasoning": map[string]string{"effort": "none"},
				}
				client := &grokClient{openCode: &service, httpClient: &http.Client{Transport: grokTestTransport(func(r *http.Request) (*http.Response, error) {
					if !strings.HasSuffix(r.URL.Path, test.path) {
						t.Fatalf("wrong endpoint: %s", r.URL)
					}
					if test.path == "/messages" {
						if r.Header.Get("x-api-key") != "go-key" || r.Header.Get("Authorization") != "" || r.Header.Get("anthropic-version") != "2023-06-01" {
							t.Error("wrong Messages auth")
						}
					} else if r.Header.Get("Authorization") != "Bearer go-key" || r.Header.Get("x-api-key") != "" {
						t.Error("wrong Responses auth")
					}
					if r.Header.Get(chatGPTAccountIDHeader) != "" {
						t.Error("Codex account forwarded")
					}
					body, _ := io.ReadAll(r.Body)
					if bytes.Contains(body, []byte("opencode-go:")) || bytes.Contains(body, []byte("_opencode_reasoning")) ||
						!bytes.Contains(body, []byte("input_schema")) && test.path == "/messages" {
						t.Errorf("invalid endpoint request: %s", body)
					}
					return serverHTTPResponse(test.fixture), nil
				})}}
				response, err := client.forwardExecution(t.Context(), t.Context(), mustTestJSON(t, request), grokTestHeaders())
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				var result map[string]jsontext.Value
				if stream {
					for _, line := range strings.Split(string(body), "\n") {
						if strings.HasPrefix(line, "data: ") {
							var event map[string]jsontext.Value
							_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event)
							if string(event["type"]) == `"response.completed"` {
								if err := json.Unmarshal(event["response"], &result); err != nil {
									t.Fatal(err)
								}
							}
						}
					}
				} else if err := json.Unmarshal(body, &result); err != nil {
					t.Fatal(err)
				}
				if len(result) == 0 || !bytes.Contains(result["usage"], []byte(`"output_tokens":4`)) {
					t.Fatalf("missing terminal/usage: %s", body)
				}
				var history []any
				if err := json.Unmarshal(result["output"], &history); err != nil {
					t.Fatal(err)
				}
				call := history[len(history)-1].(map[string]any)
				if call["type"] != "custom_tool_call" || call["input"] != "text(1)\n" {
					t.Fatalf("tool identity/input lost: %v", call)
				}
				history = append(history, map[string]string{"type": "custom_tool_call_output", "call_id": "call1", "output": "done"})
				request["input"] = history
				tr, err := translateChatRequest(mustTestJSON(t, request), &service)
				if err != nil {
					t.Fatal(err)
				}
				wire := mustMarshalJSON(tr.body)
				if !bytes.Contains(wire, []byte(test.retained)) || bytes.Contains(wire, []byte(openCodeReasoningPrefix)) ||
					!bytes.Contains(wire, []byte("call1")) {
					t.Fatalf("opaque reasoning/tool replay lost: %s", wire)
				}
				request["model"] = service.prefix + ":glm-5.3"
				if _, err := translateChatRequest(mustTestJSON(t, request), &service); err == nil {
					t.Fatal("foreign-format encrypted history accepted by Chat route")
				}
			}
		})
	}
}

func TestOpenCodeEndpointStreamsRejectPartialTools(t *testing.T) {
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	for _, test := range []struct{ model, fixture, terminal string }{
		{"minimax-m2.7", openCodeAnthropicFixture(), `data: {"type":"message_stop"}`},
		{"grok-4.6", openCodeResponsesFixture(), `data: {"response":`},
	} {
		t.Run(test.model, func(t *testing.T) {
			index := strings.LastIndex(test.fixture, test.terminal)
			if index < 0 {
				t.Fatal("fixture terminal not found")
			}
			tr, err := translateChatRequest(mustTestJSON(t, map[string]any{
				"model": service.prefix + ":" + test.model, "input": []any{},
				"tools": []any{map[string]string{"type": "custom", "name": "exec"}},
			}), &service)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tr.readProviderStream(io.NopCloser(strings.NewReader(test.fixture[:index])), func(event map[string]any) error {
				if event["type"] == "response.completed" {
					t.Error("truncated endpoint response completed")
				}
				if event["type"] == "response.output_item.done" {
					item := event["item"].(map[string]any)
					if item["type"] == "custom_tool_call" {
						t.Error("tool escaped before provider terminal")
					}
				}
				return nil
			})
			if err == nil {
				t.Fatal("truncated endpoint stream accepted")
			}
		})
	}
}

func TestOpenCodeResponsesRefusal(t *testing.T) {
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	for _, streamed := range []bool{false, true} {
		item := map[string]any{"type": "message", "id": "msg_refusal", "role": "assistant", "content": []any{
			map[string]string{"type": "refusal", "refusal": "Cannot fulfill that request."},
		}}
		fixture := ""
		if streamed {
			fixture = openCodeEvents(map[string]any{"type": "response.refusal.delta", "output_index": 0, "delta": "Cannot fulfill that request."})
		}
		fixture += openCodeEvents(map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{item}}})
		tr, err := translateChatRequest([]byte(`{"model":"opencode-go:grok-4.6","input":[]}`), &service)
		if err != nil {
			t.Fatal(err)
		}
		var events []map[string]any
		result, err := tr.readProviderStream(io.NopCloser(strings.NewReader(fixture)), func(event map[string]any) error {
			events = append(events, event)
			return nil
		})
		if err != nil || result["status"] != "completed" {
			t.Fatalf("refusal became transport failure: %v", err)
		}
		output := result["output"].([]any)
		message := output[0].(map[string]any)
		part := message["content"].([]any)[0].(map[string]any)
		if part["type"] != "refusal" || part["refusal"] != "Cannot fulfill that request." {
			t.Fatalf("refusal content lost: %v", part)
		}
		foundDone := false
		for _, event := range events {
			if event["type"] == "response.refusal.done" && event["refusal"] == part["refusal"] {
				foundDone = true
			}
		}
		if !foundDone {
			t.Fatal("missing native refusal lifecycle")
		}
		replay, err := translateChatRequest(mustTestJSON(t, map[string]any{"model": "opencode-go:grok-4.6", "input": output}), &service)
		if err != nil || !bytes.Contains(mustMarshalJSON(replay.body), []byte(`"type":"refusal"`)) {
			t.Fatalf("refusal replay lost: %v", err)
		}
	}
}

func TestOpenCodeSessionAffinity(t *testing.T) {
	first := openCodeSessionID("opencode-go", "thread-a")
	if first == "" || first != openCodeSessionID("opencode-go", "thread-a") ||
		first == openCodeSessionID("opencode-go", "thread-b") ||
		first == openCodeSessionID("opencode-zen", "thread-a") ||
		strings.Contains(first, "thread-a") || openCodeSessionID("opencode-go", "") != "" {
		t.Fatal("provider-scoped stable thread identity is not isolated")
	}
}
