package router

import (
	"errors"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestProviderBoundaryChatUsage(t *testing.T) {
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	for _, provider := range []struct {
		name    string
		service *openCodeService
		model   string
		output  int64
	}{
		{"OpenCode", &service, service.prefix + ":" + openCodeTestModel(service), 7},
		{"Grok", nil, grokModel, 10},
	} {
		for _, counts := range []struct {
			name        string
			nested, top int64
		}{
			{"nested only", 3, 0},
			{"top-level only", 0, 3},
			// The nested count wins, rather than adding or replacing it with
			// the distinct top-level count when both are present.
			{"both", 3, 5},
		} {
			t.Run(provider.name+"/"+counts.name, func(t *testing.T) {
				tr, err := translateProviderRequest(mustTestJSON(t, map[string]any{
					"model": provider.model, "input": []any{},
				}), provider.service)
				if err != nil {
					t.Fatal(err)
				}
				if tr.format != "chat" {
					t.Fatalf("expected Chat endpoint, got %s", tr.format)
				}
				usage := map[string]any{"prompt_tokens": 11, "completion_tokens": 7}
				if counts.nested != 0 {
					usage["completion_tokens_details"] = map[string]any{"reasoning_tokens": counts.nested}
				}
				if counts.top != 0 {
					usage["reasoning_tokens"] = counts.top
				}
				stream := grokTestSSE(
					map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop"}}},
					map[string]any{"choices": []any{}, "usage": usage},
				)
				var terminal map[string]any
				result, err := tr.codec.Read(strings.NewReader(stream), func(event map[string]any) error {
					if event["type"] == "response.completed" {
						terminal = event["response"].(map[string]any)
					}
					return nil
				}, openCodeTestFailureDetail)
				if err != nil {
					t.Fatal(err)
				}
				if terminal == nil || result["status"] != "completed" {
					t.Fatalf("missing successful terminal: result=%v terminal=%v", result, terminal)
				}
				for _, response := range []map[string]any{result, terminal} {
					u := response["usage"].(map[string]any)
					details := u["output_tokens_details"].(map[string]any)
					if u["input_tokens"] != int64(11) || u["output_tokens"] != provider.output ||
						u["total_tokens"] != 11+provider.output || details["reasoning_tokens"] != int64(3) {
						t.Fatalf("incorrect usage: %v", u)
					}
				}
			})
		}
	}
}

func TestProviderBoundaryOpenCodeChatFailures(t *testing.T) {
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	for _, test := range []struct {
		name, stream, code, message string
	}{
		{"invalid JSON", "data: not-json\n\n", "opencode_stream_invalid_json", "invalid JSON in OpenCode response stream"},
		{"missing DONE", strings.TrimSuffix(grokTextStream(), "data: [DONE]\n\n"), "opencode_stream_missing_done", "OpenCode stream ended without [DONE]"},
		{"missing calls", grokTestSSE(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls"}}}), "opencode_stream_missing_calls", "OpenCode finished tool calls without a call"},
		{"post-terminal choice", grokTestSSE(
			map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop"}}},
			map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": "late"}}}},
		), "opencode_stream_data_after_terminal", "OpenCode returned choice data after its terminal finish reason"},
		{"provider detail", grokTestSSE(map[string]any{"error": map[string]string{"message": "Grok quota exhausted"}}), "opencode_stream_provider_error", "OpenCode: Grok quota exhausted\nRaw response: {\"message\":\"Grok quota exhausted\"}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tr, err := translateProviderRequest(openCodeTestRequest(t, service, true), &service)
			if err != nil {
				t.Fatal(err)
			}
			if tr.format != "chat" {
				t.Fatalf("expected Chat endpoint, got %s", tr.format)
			}
			assertProviderBoundaryFailure(t, tr, test.stream, test.code, test.message)
		})
	}
}

func TestProviderBoundaryMixedRefusalDiagnostics(t *testing.T) {
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	for _, policy := range []codexresponses.Policy{grokTranslationPolicy(), openCodeTranslationPolicy(&service, openCodeTestModel(service), "chat")} {
		t.Run(policy.Diagnostics.Name, func(t *testing.T) {
			tr := testProviderRequest(t, policy, "", "chat", nil)
			stream := grokTestSSE(map[string]any{"choices": []any{map[string]any{
				"index": 0, "delta": map[string]string{"content": "text", "refusal": "refusal"},
			}}})
			// This legacy shared code remains stable even on the Grok route.
			assertProviderBoundaryFailure(t, tr, stream, "opencode_stream_invalid", "Mixed text and refusal output is unsupported")
		})
	}
}

func assertProviderBoundaryFailure(t *testing.T, tr *providerRequest, stream, code, message string) {
	t.Helper()
	var events []map[string]any
	result, err := tr.codec.Read(strings.NewReader(stream), func(event map[string]any) error {
		events = append(events, event)
		return nil
	}, openCodeTestFailureDetail)
	diagnostic, ok := errors.AsType[*criticalDiagnosticError](err)
	if result != nil || !ok || diagnostic.code != code || err.Error() != message {
		t.Fatalf("result=%v error=%v; want diagnostic %s: %s", result, err, code, message)
	}
	if len(events) < 2 || events[0]["type"] != "response.created" {
		t.Fatalf("missing creation/failure lifecycle: %v", events)
	}
	last := events[len(events)-1]
	failed, ok := last["response"].(map[string]any)
	if !ok || last["type"] != "response.failed" || failed["status"] != "failed" ||
		failed["id"] != events[0]["response"].(map[string]any)["id"] {
		t.Fatalf("invalid failed terminal: %v", last)
	}
	failure, ok := failed["error"].(map[string]string)
	if !ok || failure["code"] != code || failure["message"] != message {
		t.Fatalf("incorrect terminal diagnostic: %v", failed)
	}
	failedCount := 0
	for _, event := range events {
		switch event["type"] {
		case "response.failed":
			failedCount++
		case "response.completed", "response.output_item.done", "response.function_call_arguments.done", "response.custom_tool_call_input.done":
			t.Fatalf("failed stream exposed successful output: %v", event)
		}
	}
	if failedCount != 1 {
		t.Fatalf("got %d failure terminals, want one", failedCount)
	}
}
