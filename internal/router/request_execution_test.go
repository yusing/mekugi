package router

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeprecatedTerraRoutesToSol(t *testing.T) {
	request := modelTestRequest(t, "gpt-5.6-terra")
	headers := serverMetadataHeaders(t, "turn", nil)
	headers.Set(threadIDHeader, "terra-thread")
	attempt := newRequestAttempt(requestExecutor{provider: &serverFakeProvider{}, issues: NewCriticalErrors()}, t.Context(), t.Context(), request, headers, "")
	if err := attempt.prepare(); err != nil {
		t.Fatal(err)
	}
	if got := attempt.request.modelDescription(); got != "gpt-6-sol medium" {
		t.Fatalf("route=%q", got)
	}
}

// executeRequest preserves the direct attempt setup used throughout the package
// tests while production code owns stable services through requestExecutor.
func executeRequest(
	ctx context.Context,
	executionCtx context.Context,
	request parsedResponsesRequest,
	headers http.Header,
	sessionID string,
	provider responseProvider,
	output io.Writer,
	issues *CriticalErrors,
	mekugiCalls *mekugiProxy,
) error {
	executor := requestExecutor{
		provider: provider, output: output, issues: issues,
		mekugiCalls: mekugiCalls,
	}
	return executor.execute(ctx, executionCtx, request, headers, sessionID)
}

// Source: codex-rs/core/src/responses_metadata.rs:325:426. Canonical
// AgentControl thread-spawn metadata used by provider-boundary fixtures.
const (
	threadSpawnSubagent     = "collab_spawn"
	threadSpawnSubagentKind = "thread_spawn"
)

func modelTestRequest(t *testing.T, model string) parsedResponsesRequest {
	t.Helper()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model":     model,
		"reasoning": map[string]any{"effort": "medium", "summary": "auto"},
		"input":     []any{map[string]any{"type": "message", "role": "user", "content": "keep exact history"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestResponsesPreserveConfiguredModelAndReasoning(t *testing.T) {
	for _, model := range []string{"gpt-5.6", "gpt-5.6-sol", "gpt-5.6-luna", "gpt-6-sol", "gpt-6.1-sol", "gpt-6-luna", "gpt-6-astra"} {
		for _, child := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/child=%t/stream=%t", model, child, stream), func(t *testing.T) {
					request := modelTestRequest(t, model)
					request.fields["stream"] = mustTestJSON(t, stream)
					headers := serverMetadataHeaders(t, "turn", nil)
					if child {
						headers.Set(openAISubagentHeader, threadSpawnSubagent)
						headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, codexTurnMetadata{RequestKind: "turn", SubagentKind: threadSpawnSubagentKind})))
					}
					// Repeated completed responses exceed every old schedule threshold.
					body := `{"id":"result","status":"completed","output":[{"type":"function_call"},{"type":"function_call"},{"type":"function_call"},{"type":"message","role":"assistant"},{"type":"message","role":"assistant"}],"usage":{"input_tokens":100000}}`
					provider := &serverFakeProvider{}
					for range 3 {
						response := serverHTTPResponse(body)
						if stream {
							response = serverHTTPResponse("data: {\"type\":\"response.completed\",\"response\":" + body + "}\n\n")
							response.Header.Set("Content-Type", "text/event-stream")
						}
						provider.results = append(provider.results, serverForwardResult{response: response})
					}
					handler := responsesHandler(t.Context(), defaultRequestTimeout, provider, nil, nil)
					for range 3 {
						req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(mustTestJSON(t, request.fields)))
						req.Header = headers.Clone()
						output := httptest.NewRecorder()
						handler(output, req)
						if output.Code != http.StatusOK {
							t.Fatalf("status=%d body=%s", output.Code, output.Body.String())
						}
					}
					if len(provider.forwarded) != 3 {
						t.Fatalf("provider requests=%d want=3", len(provider.forwarded))
					}
					for _, body := range provider.forwarded {
						forwarded, err := parseResponsesRequest(body)
						if err != nil {
							t.Fatal(err)
						}
						if forwarded.model() != model {
							t.Fatalf("model=%q want=%q", forwarded.model(), model)
						}
						for _, key := range []string{"reasoning", "input"} {
							if !bytes.Equal(forwarded.fields[key], request.fields[key]) {
								t.Fatalf("%s changed: %s", key, forwarded.fields[key])
							}
						}
					}
				})
			}
		}
	}
}
