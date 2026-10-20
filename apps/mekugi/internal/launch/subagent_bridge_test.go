package launch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func bridgeTestRequest(t *testing.T, additional bool) codexresponses.Request {
	t.Helper()
	ns := map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{map[string]any{"type": "function", "name": "spawn_agent", "parameters": map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string", "encrypted": true}, "model": map[string]string{"type": "string"}}}}}}
	request := map[string]any{"model": "gpt-test", "instructions": "keep", "tools": []any{ns}, "input": []any{map[string]string{"type": "function_call", "namespace": "collaboration", "name": "spawn_agent", "arguments": "{}", "call_id": "c1"}}}
	if additional {
		request["tools"] = []any{}
		request["input"] = append(request["input"].([]any), map[string]any{"type": "additional_tools", "tools": []any{ns}})
	}
	parsed, err := codexresponses.ParseRequest(mustTestJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
func TestModelsHandlerPreservesNativeCatalogWithGrokEnabled(t *testing.T) {
	body := `{"models":[{"slug":"gpt-5.6-sol","multi_agent_version":"v2"}]}`
	client := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Etag": []string{`"native-etag"`}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	provider := newTestProviderClient(testProviderBaseURL, client)
	provider.Grok = &inferencegateway.ThirdPartyClient{Diagnostics: requestexecution.ProviderDiagnostics()}
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header = codexAuthHeaders()
	response := httptest.NewRecorder()
	inferencegateway.ModelsHandler(provider, false, (*requestexecution.Failures)(nil).ReportCatalog)(response, request)
	if response.Code != http.StatusOK || response.Body.String() != body || response.Header().Get("ETag") != `"native-etag"` {
		t.Fatalf("native catalog was rewritten: status=%d body=%s etag=%s", response.Code, response.Body.String(), response.Header().Get("ETag"))
	}
}

func TestOrdinaryCollaborationBridgeAtServerBoundary(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("mekugi=%t/stream=%t", enabled, stream), func(t *testing.T) {
				var proxy *observationFixture
				if enabled {
					proxy = newManagedMekugiProxy(t)
				}
				base := bridgeTestRequest(t, false)
				request := serverRequest(t, func(fields map[string]any) {
					fields["tools"] = json.RawMessage(base.Fields["tools"])
					fields["stream"] = stream
				})
				namespace := "collaboration"
				if enabled {
					namespace = "mekugi_collaboration"
				}
				const arguments = `{"message":"Boundary assignment","fork_turns":"none"}`
				call := map[string]any{"type": "function_call", "namespace": namespace, "name": "spawn_agent",
					"id": "spawn-item", "call_id": "spawn-call", "arguments": arguments, "status": "completed"}
				terminal := mustTestJSON(t, map[string]any{"id": "boundary", "status": "completed", "output": []any{call}})
				response := serverHTTPResponse(string(terminal))
				if stream {
					response = serverHTTPResponse(finalAnswerTestWire([][]byte{
						mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": call}),
						mustTestJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(terminal)}),
					}))
					response.Header.Set("Content-Type", "text/event-stream")
				}
				provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
				headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
				var output bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), request, headers, "bridge-session",
					provider, &output, testCriticalErrors(), proxy); err != nil {
					t.Fatal(err)
				}
				if len(provider.forwarded) != 1 {
					t.Fatalf("unexpected provider continuations: %d", len(provider.forwarded))
				}
				forwarded := provider.forwarded[0]
				if bytes.Contains(forwarded, []byte("mekugi_collaboration")) != enabled ||
					bytes.Contains(forwarded, []byte("grok:")) {
					t.Fatalf("incorrect provider catalog: %s", forwarded)
				}
				if !bytes.Contains(output.Bytes(), []byte(`"namespace":"collaboration"`)) ||
					bytes.Contains(output.Bytes(), []byte("mekugi_collaboration")) ||
					bytes.Contains(output.Bytes(), []byte(`"encrypted_function_args":[]`)) != enabled ||
					!bytes.Contains(output.Bytes(), mustTestJSON(t, arguments)) {
					t.Fatalf("incorrect native output: %s", output.Bytes())
				}
			})
		}
	}
}
