package router

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestThreadUsageIgnoresMalformedAuxiliaryIdentity(t *testing.T) {
	for _, requestKind := range []string{"turn", "compaction"} {
		t.Run(requestKind, func(t *testing.T) {
			for _, field := range []string{"agent_name", "parent_thread_id"} {
				t.Run(field, func(t *testing.T) {
					proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
					request := serverRequest(t, nil)
					headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
					if requestKind == "compaction" {
						headers = serverCompactionMetadataHeaders(t)
						rawMetadata := headers[codexTurnMetadataHeader][0]
						delete(headers, codexTurnMetadataHeader)
						headers.Set(codexTurnMetadataHeader, rawMetadata)
						var err error
						request, err = parseResponsesRequest(mustTestJSON(t, map[string]any{
							"model": "gpt-test", "input": []any{map[string]any{"role": "user", "content": "compact"}},
							"stream": true, "tool_choice": "auto", "parallel_tool_calls": false,
						}))
						if err != nil {
							t.Fatal(err)
						}
					}
					headers.Set(threadIDHeader, "stable-thread")
					var metadata map[string]json.RawMessage
					if err := json.Unmarshal([]byte(headers.Get(codexTurnMetadataHeader)), &metadata); err != nil {
						t.Fatal(err)
					}
					metadata[field] = json.RawMessage(`42`)
					headers.Set(codexTurnMetadataHeader, string(mustMarshalJSON(metadata)))
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
					if err := executeRequest(t.Context(), t.Context(), request, headers, "session", provider, &output, nil, proxy, nil, nil); err != nil {
						t.Fatal(err)
					}
					got, valid := proxy.usage.snapshot("stable-thread")
					if !valid || got.tokenCounts != (tokenCounts{InputTokens: 12, UncachedInputTokens: 7, OutputTokens: 7, ReasoningTokens: 3}) {
						t.Fatal("auxiliary identity lost authoritative thread usage", got, valid)
					}
				})
			}
		})
	}
}
func TestThreadUsageConflictDoesNotResumeWithPartialTotals(t *testing.T) {
	totals := newThreadUsage()
	totals.observation("transport", "", "gpt-5.5", "").observe(tokenCounts{InputTokens: 10})
	totals.observation("other", "", "gpt-5.5", "").observe(tokenCounts{InputTokens: 20})
	totals.observation("transport", "other", "gpt-5.5", "").observe(tokenCounts{InputTokens: 30})
	totals.observation("transport", "transport", "gpt-5.5", "").observe(tokenCounts{InputTokens: 40})
	if _, valid := totals.snapshot("transport"); valid {
		t.Fatal("conflicting identity later reported a partial lifetime total")
	}
	if got, valid := totals.snapshot("other"); !valid || got.InputTokens != 20 {
		t.Fatal("conflicting metadata changed another transport thread", got, valid)
	}
}
