package executionobservation

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestApprovalReviewPreservesNativeRequestAndChildAdmission(t *testing.T) {
	for _, kind := range []string{"guardian", "internal", "thread_spawn"} {
		t.Run(kind, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			driver, err := threadbinding.NewDriver(control.New())
			if err != nil {
				t.Fatal(err)
			}
			proxy.threadControl = driver
			request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
				"model": "gpt-test", "instructions": "Review the proposed action under the approval policy.",
				"input": []any{map[string]any{"role": "user", "content": "Review this action."}},
				"tools": testExecResponsesTools(), "tool_choice": "auto",
				"access_programs": map[string]string{"cyber": "standard"},
				"text":            map[string]any{"format": map[string]any{"type": "json_schema", "name": "review", "schema": map[string]any{"type": "object"}}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			original, err := request.WireBody(request.Fields)
			if err != nil {
				t.Fatal(err)
			}
			headers := make(http.Header)
			headers.Set(codexresponses.ThreadIDHeader, "review-thread")
			headers.Set(codexresponses.RoutingSessionHeader, "review-session")
			metadata := map[string]any{"thread_id": "review-thread", "turn_id": "review-turn", "request_kind": "turn", "workspaces": map[string]any{t.TempDir(): nil}}
			if kind == "internal" {
				headers.Set(codexresponses.GuardianHeader, "reviewer")
				headers.Set(codexresponses.SubagentHeader, "guardian")
			} else {
				metadata["subagent_kind"] = kind
			}
			headers.Set(codexresponses.TurnMetadataHeader, string(mustTestJSON(t, metadata)))
			const response = `{"id":"review-result","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"denied"}]}]}`
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(response)}}}
			executor := testRequestExecutor(provider, nil, proxy)
			var output bytes.Buffer
			err = executor.ExecuteHTTP(t.Context(), t.Context(), request, headers, &output)
			if kind == "thread_spawn" {
				if err == nil || !strings.Contains(err.Error(), "independent child request preceded logical admission") || len(provider.forwarded) != 0 {
					t.Fatalf("unadmitted child escaped admission: %v, forwards=%d", err, len(provider.forwarded))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 1 || !bytes.Equal(provider.forwarded[0], original) {
				t.Fatalf("native reviewer request changed: %s", provider.forwarded)
			}
			if !strings.Contains(output.String(), `"text":"denied"`) {
				t.Fatalf("review decision changed: %s", output.String())
			}
			if _, admitted := driver.Coordination().Member("review-thread"); admitted {
				t.Fatal("reviewer entered managed-agent tree")
			}
		})
	}
}
