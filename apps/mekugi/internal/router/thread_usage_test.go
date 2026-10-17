package router

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/yusing/mekugi/contracts/inference"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestThreadUsageSurvivesRoundTripsAndSessionRemapping(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	first, _ := prepareActivityTest(t, proxy, "session-a", "root-thread", "", "/root", nil)
	first.ObserveUsage(inference.Usage{InputTokens: 10, UncachedInputTokens: 4, OutputTokens: 3, ReasoningTokens: 2})
	first.ObserveUsage(inference.Usage{InputTokens: 10, UncachedInputTokens: 4, OutputTokens: 3, ReasoningTokens: 2})
	first.Close()
	child, _ := prepareActivityTest(t, proxy, "session-a", "child-thread", "root-thread", "/root/child", nil)
	child.ObserveUsage(inference.Usage{InputTokens: 500})
	next, _ := prepareActivityTest(t, proxy, "session-remapped", "root-thread", "", "/root", nil)
	second := inference.Usage{InputTokens: 20, UncachedInputTokens: 5, OutputTokens: 7, ReasoningTokens: 6}
	next.ObserveUsage(second)
	got, valid := proxy.usage.Snapshot("root-thread")
	if !valid || got.Usage != (inference.Usage{InputTokens: 30, UncachedInputTokens: 9, OutputTokens: 10, ReasoningTokens: 8}) {
		t.Fatal("root lifetime counts changed across round trips", got, valid)
	}

	if got, valid := proxy.usage.Snapshot("child-thread"); !valid || got.InputTokens != 500 {
		t.Fatal("root and child totals mixed", got, valid)
	}
	next.Close()
	_, _ = prepareActivityTest(t, proxy, "later-session", "root-thread", "", "/root", nil)
	if laterCounts, valid := proxy.usage.Snapshot("root-thread"); !valid || laterCounts != got {
		t.Fatal("completed response lifetime reset totals", laterCounts, valid)
	}
}

func TestThreadUsageIncludesCompactionWithoutRewritingIt(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	first, _ := prepareActivityTest(t, proxy, "before", "root-thread", "", "/root", nil)
	first.ObserveUsage(inference.Usage{InputTokens: 10, UncachedInputTokens: 4, OutputTokens: 3, ReasoningTokens: 2})
	first.Close()
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": []any{map[string]any{"type": "message", "role": "user", "content": "compact this"}},
		"stream": true, "tool_choice": "auto", "parallel_tool_calls": false,
	}))
	if err != nil {
		t.Fatal(err)
	}
	terminal := mustTestJSON(t, map[string]any{
		"type": "response.completed", "response": map[string]any{
			"id": "compaction", "status": "completed", "output": []any{},
			"usage": map[string]any{
				"input_tokens": 12, "input_tokens_details": map[string]any{"cached_tokens": 5},
				"output_tokens": 7, "output_tokens_details": map[string]any{"reasoning_tokens": 3},
			},
		},
	})
	responseBody := "event: response.completed\ndata: " + string(terminal) + "\n\n"
	response := serverHTTPResponse(responseBody)
	response.Header.Set("Content-Type", "text/event-stream")
	provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
	headers := serverCompactionMetadataHeaders(t)
	headers.Set(codexresponses.ThreadIDHeader, "root-thread")
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "compaction-session", provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if output.String() != responseBody {
		t.Fatal("compaction usage accounting rewrote provider output", output.String())
	}
	next, _ := prepareActivityTest(t, proxy, "after-remap", "root-thread", "", "/root", nil)
	next.ObserveUsage(inference.Usage{InputTokens: 1, UncachedInputTokens: 1})
	if got, valid := proxy.usage.Snapshot("root-thread"); !valid || got.Usage != (inference.Usage{InputTokens: 23, UncachedInputTokens: 12, OutputTokens: 10, ReasoningTokens: 5}) {
		t.Fatal("compaction tokens missing from lifetime total", got, valid)
	}
	var forwarded map[string]json.RawMessage
	if len(provider.forwarded) != 1 || json.Unmarshal(provider.forwarded[0], &forwarded) != nil || !bytes.Equal(forwarded["input"], request.Fields["input"]) {
		t.Fatal("compaction request changed")
	}
}
