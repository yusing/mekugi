package inferencegateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/providers/openai"
	"github.com/yusing/mekugi/providers/opencode"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func testProviderRequest(t *testing.T, policy codexresponses.Policy, model, format string, tools []any) *providerRequest {
	t.Helper()
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": []any{}, "tools": tools}))
	if err != nil {
		t.Fatal(err)
	}
	body, codec, err := codexresponses.Translate(request.Fields, model, format, policy)
	if err != nil {
		t.Fatal(err)
	}
	return &providerRequest{body: body, format: format, codec: codec}
}

func testOpenCodeTranslation(t *testing.T, service *opencode.Service, model, format string, tools []any) *providerRequest {
	return testProviderRequest(t, testOpenCodePolicy(service, model, format), model, format, tools)
}

func grokTestFailureDetail(body []byte) string { return NewProviderHTTPError("Grok", 0, body).Error() }
func openCodeTestFailureDetail(body []byte) string {
	return NewProviderHTTPError("OpenCode", 0, body).Error()
}

const grokModel = "grok:grok-4.6"

func grokTestRequest(t *testing.T, stream bool) []byte {
	t.Helper()
	return mustTestJSON(t, map[string]any{"model": grokModel, "stream": stream, "instructions": "test instructions", "reasoning": map[string]string{"effort": "low"}, "tools": []any{map[string]any{"type": "custom", "name": "exec", "description": "Run exact input"}}, "input": []any{map[string]any{"type": "message", "role": "user", "author": "/root", "recipient": "/root/probe", "content": []any{map[string]string{"type": "input_text", "text": "Run the probe"}}}}})
}

func grokTestHeaders() http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer codex-secret")
	headers.Set(openai.AccountIDHeader, "codex-account")
	headers.Set(codexresponses.SubagentHeader, "collab_spawn")
	headers.Set(codexresponses.ThreadIDHeader, "child-thread")
	headers.Set(codexresponses.TurnMetadataHeader, `{"subagent_kind":"thread_spawn","request_kind":"regular"}`)
	return headers
}

func grokTestSSE(chunks ...any) string {
	var out strings.Builder
	for _, chunk := range chunks {
		fmt.Fprintf(&out, "data: %s\n\n", responses.MustMarshalJSON(chunk))
	}
	out.WriteString("data: [DONE]\n\n")
	return out.String()
}

func grokTextStream() string {
	return grokTestSSE(map[string]any{"model": "grok-4.6-build", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": "GROK_OK"}, "finish_reason": "stop"}}}, map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4, "prompt_tokens_details": map[string]int{"cached_tokens": 3}, "completion_tokens_details": map[string]int{"reasoning_tokens": 2}}})
}

type grokTestTransport func(*http.Request) (*http.Response, error)

func (f grokTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mustTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
