package launch

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/yusing/mekugi/contracts/inference"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestThreadUsageIgnoresMalformedAuxiliaryIdentity(t *testing.T) {
	for _, requestKind := range []string{"turn", "compaction"} {
		t.Run(requestKind, func(t *testing.T) {
			for _, field := range []string{"agent_name", "parent_thread_id"} {
				t.Run(field, func(t *testing.T) {
					proxy := newManagedMekugiProxy(t)
					request := serverRequest(t, nil)
					headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
					if requestKind == "compaction" {
						headers = serverCompactionMetadataHeaders(t)
						var err error
						request, err = codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
							"model": "gpt-test", "input": []any{map[string]any{"role": "user", "content": "compact"}},
							"stream": true, "tool_choice": "auto", "parallel_tool_calls": false,
						}))
						if err != nil {
							t.Fatal(err)
						}
					}
					headers.Set(codexresponses.ThreadIDHeader, "stable-thread")
					var metadata map[string]json.RawMessage
					if err := json.Unmarshal([]byte(headers.Get(codexresponses.TurnMetadataHeader)), &metadata); err != nil {
						t.Fatal(err)
					}
					metadata[field] = json.RawMessage(`42`)
					headers.Set(codexresponses.TurnMetadataHeader, string(responses.MustMarshalJSON(metadata)))
					body := mustTestJSON(t, map[string]any{
						"id": "roundtrip", "status": "completed", "output": []any{},
						"usage": map[string]any{"input_tokens": 12, "input_tokens_details": map[string]any{"cached_tokens": 5}, "output_tokens": 7, "output_tokens_details": map[string]any{"reasoning_tokens": 3}},
					})
					response := serverHTTPResponse(string(body))
					if requestKind == "compaction" {
						event := mustTestJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(body)})
						response = serverHTTPResponse("event: response.completed\ndata: " + string(event) + "\n\n")
						response.Header.Set("Content-Type", "text/event-stream")
					}
					provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
					var output bytes.Buffer
					if err := executeRequest(t.Context(), t.Context(), request, headers, "session", provider, &output, nil, proxy); err != nil {
						t.Fatal(err)
					}
					got, valid := proxy.usage.Snapshot("stable-thread")
					if !valid || got.Usage != (inference.Usage{InputTokens: 12, UncachedInputTokens: 7, OutputTokens: 7, ReasoningTokens: 3}) {
						t.Fatal("auxiliary identity lost authoritative thread usage", got, valid)
					}
				})
			}
		})
	}
}
