package router

import (
	"bytes"
	"strings"
	"testing"
)

func TestMainCompletionDoesNotPersistPriorTotalsWithoutCurrentUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			prior, _ := prepareActivityTest(t, proxy, "before", "thread", "", "/root", nil)
			prior.observeResponseUsage(tokenCounts{InputTokens: 20, UncachedInputTokens: 8, OutputTokens: 5, ReasoningTokens: 3})
			prior.Close()
			final, _ := prepareActivityTest(t, proxy, "after", "thread", "", "/root", nil)
			response := map[string]any{
				"id": "answer", "status": "completed",
				"output": []any{map[string]any{"type": "message", "role": "assistant", "phase": "final_answer",
					"content": []any{map[string]any{"type": "output_text", "text": "Actual answer"}}}},
			}
			var output []byte
			if stream {
				events, err := final.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": response}))
				if err != nil {
					t.Fatal(err)
				}
				output = bytes.Join(events, nil)
			} else {
				var err error
				output, err = final.TransformJSON(mustTestJSON(t, response))
				if err != nil {
					t.Fatal(err)
				}
			}
			if final.usageObserved {
				t.Fatal("test unexpectedly observed current usage")
			}
			if strings.Contains(string(output), "Router session usage") || !strings.Contains(string(output), "Actual answer") {
				t.Fatalf("stale token usage was emitted or provider answer was lost: %s", output)
			}
			if _, available := final.threadUsageCounts(); !available {
				t.Fatal("prior aggregate was lost")
			}
			if paths := proxy.tokenMetricPaths(); len(paths) != 0 {
				t.Fatalf("completion persisted metrics without current usage: %q", paths)
			}
		})
	}
}
