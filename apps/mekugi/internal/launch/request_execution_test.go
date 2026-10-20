package launch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestDeprecatedTerraRoutesToSol(t *testing.T) {
	request := modelTestRequest(t, "gpt-5.6-terra")
	headers := serverMetadataHeaders(t, "turn", nil)
	headers.Set(codexresponses.ThreadIDHeader, "terra-thread")
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
	executor := testRequestExecutor(provider, testCriticalErrors(), nil)
	executor.Output = io.Discard
	if err := executor.Execute(t.Context(), t.Context(), request, headers, ""); err != nil {
		t.Fatal(err)
	}
	forwarded, err := codexresponses.ParseRequest(provider.forwarded[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(forwarded.Model() + " " + forwarded.ReasoningEffort()); got != "gpt-6-sol medium" {
		t.Fatalf("route=%q", got)
	}

}

// executeRequest preserves the direct attempt setup used throughout the package
// tests while production code owns stable services through requestexecution.Executor.
func executeRequest(
	ctx context.Context,
	executionCtx context.Context,
	request codexresponses.Request,
	headers http.Header,
	sessionID string,
	provider inferencegateway.Forwarder,
	output io.Writer,
	issues *requestexecution.Failures,
	mekugiCalls *observationFixture,
) error {
	executor := testRequestExecutor(provider, issues, mekugiCalls)
	executor.Output = output
	return executor.Execute(ctx, executionCtx, request, headers, sessionID)
}

// Source: codex-rs/core/src/responses_metadata.rs:325:426. Canonical
// AgentControl thread-spawn metadata used by provider-boundary fixtures.
const (
	threadSpawnSubagent     = "collab_spawn"
	threadSpawnSubagentKind = "thread_spawn"
)

func modelTestRequest(t *testing.T, model string) codexresponses.Request {
	t.Helper()
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
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
					request.Fields["stream"] = mustTestJSON(t, stream)
					headers := serverMetadataHeaders(t, "turn", nil)
					if child {
						headers.Set(codexresponses.SubagentHeader, threadSpawnSubagent)
						headers.Set(codexresponses.TurnMetadataHeader, string(mustTestJSON(t, codexresponses.TurnMetadata{RequestKind: "turn", SubagentKind: threadSpawnSubagentKind})))
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
					handler := testResponsesHandler(t.Context(), launchoptions.DefaultRequestTimeout, provider, nil, nil)
					for range 3 {
						req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(mustTestJSON(t, request.Fields)))
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
						forwarded, err := codexresponses.ParseRequest(body)
						if err != nil {
							t.Fatal(err)
						}
						if forwarded.Model() != model {
							t.Fatalf("model=%q want=%q", forwarded.Model(), model)
						}
						for _, key := range []string{"reasoning", "input"} {
							if !bytes.Equal(forwarded.Fields[key], request.Fields[key]) {
								t.Fatalf("%s changed: %s", key, forwarded.Fields[key])
							}
						}
					}
				})
			}
		}
	}
}
