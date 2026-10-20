package inferencegateway

import (
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/yusing/mekugi/providers/opencode"

	json "encoding/json/v2"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestOpenCodeRequestDiagnosticIdentity(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	request := mustTestJSON(t, map[string]any{
		"model":                service.Prefix + ":" + openCodeTestModel(service),
		"previous_response_id": "old",
		"input":                []any{},
	})
	_, err := testTranslateProviderRequest(request, &service)
	if err == nil || !strings.Contains(err.Error(), "OpenCode requires explicit conversation history") ||
		strings.Contains(err.Error(), "grok requires") {
		t.Fatalf("OpenCode error lost provider identity: %v", err)
	}
}

func TestOpenCodeReasoningToolReplay(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	tr, err := testTranslateProviderRequest(openCodeTestRequest(t, service, false), &service)
	if err != nil {
		t.Fatal(err)
	}
	stream := grokTestSSE(
		map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"reasoning_content": "Think "}}}},
		map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"reasoning_content": "first", "tool_calls": []any{map[string]any{"index": 0, "id": "call1", "function": map[string]string{"name": "exec", "arguments": `{"input":"exact\nprogram"}`}}}}, "finish_reason": "tool_calls"}}},
	)
	result, err := tr.codec.Read(strings.NewReader(stream), func(map[string]any) error { return nil }, openCodeTestFailureDetail)
	if err != nil {
		t.Fatal(err)
	}
	input := result["output"].([]any)
	input = append(input, map[string]string{"type": "custom_tool_call_output", "call_id": "call1", "output": "done"})
	request := map[string]any{
		"model": service.Prefix + ":" + openCodeTestModel(service),
		"input": input,
		"tools": []any{map[string]string{"type": "custom", "name": "exec"}},
	}
	parsed, err := codexresponses.ParseRequest(mustTestJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	body, err := parsed.WireBody(parsed.Fields)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := testTranslateProviderRequest(body, &service)
	if err != nil {
		t.Fatal(err)
	}
	messages := replay.body["messages"].([]map[string]any)
	if len(messages) != 2 || messages[0]["reasoning_content"] != "Think first" || messages[1]["tool_call_id"] != "call1" {
		t.Fatalf("reasoning/tool replay lost: %v", messages)
	}
}

func TestOpenCodeRejectsUnsupportedHistoryAndModels(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	for _, input := range []string{
		`[{"type":"reasoning","encrypted_content":"private"}]`,
		`[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"private"}]}]`,
	} {
		_, err := testTranslateProviderRequest([]byte(`{"model":"`+service.Prefix+`:`+openCodeTestModel(service)+`","input":`+input+`}`), &service)
		diagnostic, ok := errors.AsType[*CompatibilityError](err)
		if !ok || diagnostic.Code != "opencode_encrypted_history" || strings.Contains(err.Error(), "private") {
			t.Fatalf("wrong local compatibility error: %v", err)
		}
	}
	for _, model := range []string{"opencode-go:unsupported", "opencode-zen:unsupported", grokModel} {
		if _, err := testTranslateProviderRequest([]byte(`{"model":"`+model+`","input":[]}`), &service); err == nil {
			t.Fatalf("accepted unsupported model %q", model)
		}
	}
}

func TestOpenCodeInheritedReasoningEffort(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	model := ""
	for _, candidate := range service.Models() {
		if len(candidate.Efforts) == 0 && candidate.Format == "chat" {
			model = candidate.ID
			break
		}
	}
	if model == "" {
		t.Fatal("embedded snapshot has no non-reasoning Chat model")
	}
	for _, effort := range []string{"", "none", "high", "max"} {
		var request map[string]any
		if err := json.Unmarshal(openCodeTestRequest(t, service, false), &request); err != nil {
			t.Fatal(err)
		}
		request["model"] = service.Prefix + ":" + model
		request["reasoning"] = map[string]string{"effort": effort}
		tr, err := testTranslateProviderRequest(mustTestJSON(t, request), &service)
		if err != nil {
			t.Fatalf("inherited effort requires manual clearing: %v", err)
		}
		if _, ok := tr.body["reasoning_effort"]; ok {
			t.Fatal("unadvertised effort sent to provider")
		}
	}
}

func TestOpenCodeMixedReasoningMultiTurnReplay(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	var history []any
	for _, answer := range []string{"first answer", "second answer"} {
		history = append(history, map[string]string{"role": "user", "content": "Question"})
		tr, err := testTranslateProviderRequest(mustTestJSON(t, map[string]any{
			"model": service.Prefix + ":" + openCodeTestModel(service), "input": history,
		}), &service)
		if err != nil {
			t.Fatal(err)
		}
		result, err := tr.codec.Read(strings.NewReader(grokTestSSE(
			map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{
				"reasoning_content": "reasoning for " + answer, "content": answer,
			}, "finish_reason": "stop"}}},
		)), func(map[string]any) error { return nil }, openCodeTestFailureDetail)
		if err != nil {
			t.Fatal(err)
		}
		history = append(history, result["output"].([]any)...)
	}
	// JSON persistence and a fresh translation must associate reasoning with
	// each original assistant, not just the latest tool-calling turn.
	saved := mustTestJSON(t, map[string]any{"model": service.Prefix + ":" + openCodeTestModel(service), "input": history})
	tr, err := testTranslateProviderRequest(saved, &service)
	if err != nil {
		t.Fatal(err)
	}
	messages := tr.body["messages"].([]map[string]any)
	if len(messages) != 4 || messages[1]["reasoning_content"] != "reasoning for first answer" ||
		messages[3]["reasoning_content"] != "reasoning for second answer" {
		t.Fatalf("multi-turn reasoning association lost: %v", messages)
	}
}

func TestEveryEmbeddedGoModelHasAnEndpoint(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	body, err := os.ReadFile("../../../../providers/opencode/snapshot/opencode-go-models.json")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil || len(list.Data) == 0 {
		t.Fatalf("embedded model list: %v", err)
	}
	for _, item := range list.Data {
		t.Run(item.ID, func(t *testing.T) {
			tr, err := testTranslateProviderRequest(mustTestJSON(t, map[string]any{
				"model": "opencode-go:" + item.ID, "input": []any{map[string]string{"role": "user", "content": "task"}},
				"tools": []any{map[string]string{"type": "custom", "name": "exec"}},
			}), &service)
			if service.Format(item.ID) == "" {
				if err == nil {
					t.Fatal("model without provider format was advertised")
				}
				return
			}
			if err != nil || tr.body["model"] != item.ID {
				t.Fatalf("Go model unavailable: %v", err)
			}
			switch tr.format {
			case "chat":
				if tr.body["messages"] == nil {
					t.Fatal("Chat endpoint body missing messages")
				}
			case "anthropic":
				if tr.body["max_tokens"] == nil {
					t.Fatal("Anthropic endpoint body missing max_tokens")
				}
			case "responses":
				if tr.body["input"] == nil {
					t.Fatal("Responses endpoint body missing input")
				}
			default:
				t.Fatalf("unsupported endpoint adapter %T", tr.format)
			}
		})
	}
}

func openCodeTestModel(service opencode.Service) string {
	const id = "glm-5.3-flash"
	if _, ok := service.Model(id); !ok {
		panic("embedded OpenCode test model is unavailable")
	}
	return id
}

func openCodeTestRequest(t *testing.T, service opencode.Service, stream bool) []byte {
	t.Helper()
	return mustTestJSON(t, map[string]any{
		"model": service.Prefix + ":" + openCodeTestModel(service), "stream": stream,
		"input": []any{map[string]string{"role": "user", "content": "Hello"}},
		"tools": []any{map[string]string{"type": "custom", "name": "exec"}},
	})
}

func TestProviderHostedSearchGuidanceMatchesRoute(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	for _, tc := range []struct {
		model   string
		service *opencode.Service
	}{
		{"grok-4.7", nil},
		{service.Prefix + ":" + openCodeTestModel(service), &service},
	} {
		t.Run(tc.model, func(t *testing.T) {
			request := mustTestJSON(t, map[string]any{
				"model": tc.model, "instructions": "Keep caller policy.", "input": []any{},
				"tools": []any{map[string]string{"type": "web_search"}},
			})
			translated, err := testTranslateProviderRequest(request, tc.service)
			if err != nil {
				t.Fatal(err)
			}
			wire := string(mustTestJSON(t, translated.body))
			if !strings.Contains(wire, "Keep caller policy.") || !strings.Contains(wire, "OpenAI-hosted web search is unavailable on this route.") {
				t.Fatalf("missing caller or route-neutral search guidance: %s", wire)
			}
		})
	}
}
