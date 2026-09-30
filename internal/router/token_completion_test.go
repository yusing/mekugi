package router

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainCompletionPreservesPriorTotalsWithoutCurrentUsage(t *testing.T) {
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
			if _, available := proxy.usage.snapshot(final.usageTracker.thread); !available {
				t.Fatal("prior aggregate was lost")
			}
		})
	}
}

func TestMainCompletionDoesNotExportTokenMetrics(t *testing.T) {
	for _, journal := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			name := map[bool]string{false: "plain", true: "journal"}[journal] + "/" + map[bool]string{false: "json", true: "sse"}[stream]
			t.Run(name, func(t *testing.T) {
				directory := t.TempDir()
				t.Setenv("TMPDIR", directory)
				proxy := newManagedMekugiProxy(t)
				main, _ := prepareActivityTest(t, proxy, "session", "thread", "", "/root", nil)
				main.journalActive = journal
				main.observeResponseUsage(tokenCounts{InputTokens: 100, UncachedInputTokens: 40, OutputTokens: 10})
				response := []byte(`{"id":"main-response","status":"completed","output":[{"type":"message","id":"answer","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Actual answer"}]}]}`)
				var output []byte
				if stream {
					events, err := main.TransformSSE([]byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"answer","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Actual answer"}]}}`))
					if err != nil {
						t.Fatal(err)
					}
					output = bytes.Join(events, nil)
					events, err = main.TransformSSE(append(append([]byte(`{"type":"response.completed","response":`), response...), '}'))
					if err != nil {
						t.Fatal(err)
					}
					output = append(output, bytes.Join(events, nil)...)
				} else {
					var err error
					output, err = main.TransformJSON(response)
					if err != nil {
						t.Fatal(err)
					}
				}
				main.Close()
				if !bytes.Contains(output, []byte("Actual answer")) || bytes.Contains(output, []byte("Router session usage")) {
					t.Fatalf("completion changed the answer or emitted metrics: %s", output)
				}
				report, ok := proxy.usage.snapshot("thread")
				if !ok || report.InputTokens != 100 || report.OutputTokens != 10 {
					t.Fatalf("completion lost in-memory usage: %+v, ok=%v", report, ok)
				}
				assertNoLegacyTokenMetrics(t, directory)
			})
		}
	}
}

func assertNoLegacyTokenMetrics(t *testing.T, directory string) {
	t.Helper()
	for _, pattern := range []string{"mekugi-token-metrics-*.md", ".mekugi-token-metrics-*.md"} {
		paths, err := filepath.Glob(filepath.Join(directory, pattern))
		if err != nil || len(paths) != 0 {
			t.Fatalf("unexpected legacy token metrics files: %q, error=%v", paths, err)
		}
	}
}
