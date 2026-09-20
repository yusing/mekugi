package router

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Compare the provider input with a live continuation, rather than comparing
// cache diagnostics across processes: capture baselines themselves are ephemeral.
func TestReplayContinuationMatchesLiveProviderInput(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{"native", "ctp2"} {
		for _, carrier := range []string{"native", "code-mode"} {
			for _, tool := range []string{"hpatch", "shell"} {
				t.Run(protocol+"/"+carrier+"/"+tool, func(t *testing.T) {
					workspace, storage := t.TempDir(), t.TempDir()
					translations := 0
					newProxy := func() *mekugiProxy {
						proxy := newManagedMekugiProxy(t)
						var err error
						proxy.replayStore, err = openMekugiReplayStore(storage)
						if err != nil {
							t.Fatal(err)
						}
						return proxy
					}
					proxy := newProxy()
					var codec *ctp2Codec
					if protocol == "ctp2" {
						codec = mustCTP2Codec(t)
					}
					modelCall := testMekugiItem()
					modelCall["name"] = tool
					if tool == "shell" {
						modelCall["input"] = "cat > replay.txt <<'EOF'\nexact original text\nEOF"
					}
					provider := &serverFakeProvider{results: []serverForwardResult{{
						response: serverHTTPResponse(string(mustTestJSON(t, map[string]any{
							"status": "completed", "output": []any{modelCall},
						}))),
					}}}
					headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{workspace: nil})
					request := func(history []json.RawMessage) parsedResponsesRequest {
						return serverRequest(t, func(fields map[string]any) {
							fields["instructions"] = stockModelInstructionsForTest("", "")
							if carrier == "native" {
								fields["tools"] = testNativeResponsesTools()
								fields["input"] = []any{map[string]any{"role": "user", "content": "task"}}
							}
							input := fields["input"].([]any)
							for _, item := range history {
								input = append(input, item)
							}
							fields["input"] = input
						})
					}
					var output bytes.Buffer
					first := request(nil)
					if err := executeRequest(t.Context(), t.Context(), first, headers, "parent", provider, &output, nil, proxy, codec, nil); err != nil {
						t.Fatal(err)
					}
					var response struct{ Output []json.RawMessage }
					if err := json.Unmarshal(output.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					outputType := "custom_tool_call_output"
					if carrier == "native" {
						outputType = "function_call_output"
					}
					history := append(response.Output, mustTestJSON(t, map[string]any{
						"type": outputType, "call_id": "call-H",
						"output": strings.Repeat("identical visible result with enough text to compact; ", 40),
					}))
					beforeReplay := translations
					var baseline json.RawMessage
					for _, continuation := range []string{"live", "fork", "resume", "resumed-fork"} {
						if continuation == "resume" {
							if err := proxy.Close(); err != nil {
								t.Fatal(err)
							}
							proxy = newProxy()
						}
						thread := "thread-1"
						if strings.Contains(continuation, "fork") {
							thread = "fork-thread"
						}
						headers.Set(threadIDHeader, thread)
						provider.results = append(provider.results, serverForwardResult{response: journalFinishResponse(t, false, "completed", "full", journalFinishCall(`{"op":"finish"}`))})
						output.Reset()
						if err := executeRequest(t.Context(), t.Context(), request(history), headers, continuation, provider, &output, nil, proxy, codec, nil); err != nil {
							t.Fatalf("%s: %v", continuation, err)
						}
						var forwarded map[string]json.RawMessage
						if err := json.Unmarshal(provider.forwarded[len(provider.forwarded)-1], &forwarded); err != nil {
							t.Fatal(err)
						}
						if continuation == "live" {
							baseline = bytes.Clone(forwarded["input"])
						} else if !bytes.Equal(baseline, forwarded["input"]) {
							t.Fatalf("%s changed the provider-visible replay prefix", continuation)
						}
						if translations != beforeReplay {
							t.Fatalf("%s reevaluated a historical call", continuation)
						}
					}
				})
			}
		}
	}
}
