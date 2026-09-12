package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestTokenCostServiceTiers(t *testing.T) {
	for _, tc := range []struct {
		model, tier              string
		input                    uint64
		uncached, cached, output float64
		known                    bool
	}{
		{"gpt-6-astra", "default", 200_000, 1, .1, .5, true},
		{"gpt-6-astra", "priority", 200_000, 2, .2, 1, true},
		{"openai/gpt-6-astra", "fast", 200_000, 2, .2, 1, true},
		{"gpt-6-astra", "fast", 272_000, 2, .344, 1, true},
		{"gpt-6-astra", "fast", 272_001, 4, .688004, 1.5, true},
		{"gpt-5.6-sol", "priority", 200_000, .8, .08, .4, true},
		{"gpt-5.6-sol", "fast", 272_000, .8, .1376, .4, true},
		{"gpt-5.6-sol", "fast", 272_001, 1.6, .2752016, .6, true},
		{"gpt-5.6-terra", "fast", 200_000, .4, .04, .24, true},
		{"gpt-5.6-terra", "priority", 272_001, .8, .1376008, .36, true},
		{"gpt-5.6-luna", "priority", 200_000, .04, .004, .024, true},
		{"gpt-5.6-luna", "fast", 272_001, .08, .01376008, .036, true},
		{"gpt-5.5", "fast", 271_999, 1.25, .21499875, .75, true},
		{"gpt-5.5", "priority", 272_000, 1.25, .215, .75, true},
		{"gpt-5.5", "priority", 272_001, 0, 0, 0, false},
		{"gpt-5.4", "priority", 271_999, .5, .0859995, .3, true},
		{"gpt-5.4", "fast", 272_000, .5, .086, .3, true},
		{"gpt-5.4", "fast", 272_001, 0, 0, 0, false},
		{"gpt-5.4-mini", "fast", 272_000, .15, .0258, .09, true},
		{"gpt-5.4-nano", "priority", 200_000, 0, 0, 0, false},
		{"gpt-6-astra-pro", "fast", 200_000, 0, 0, 0, false},
		{"gpt-6-astra", "auto", 200_000, 0, 0, 0, false},
		{"gpt-6-astra", "flex", 200_000, 0, 0, 0, false},
		{"gpt-6-astra", "unknown", 200_000, 0, 0, 0, false},
	} {
		t.Run(fmt.Sprintf("%s/%s/%d", tc.model, tc.tier, tc.input), func(t *testing.T) {
			got := estimateTokenCost(tc.model, tc.tier, tokenCounts{InputTokens: tc.input, UncachedInputTokens: 100_000, OutputTokens: 10_000, ReasoningTokens: 5_000})
			if got.known != tc.known || math.Abs(got.uncachedInput-tc.uncached) > 1e-10 || math.Abs(got.cachedInput-tc.cached) > 1e-10 || math.Abs(got.output-tc.output) > 1e-10 {
				t.Fatalf("cost=%+v, want %+v", got, tc)
			}
		})
	}
}

func TestTokenUsageServiceTierAcrossTransports(t *testing.T) {
	for _, cacheWrites := range []uint64{0, 20_000} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct{ requested, served, want string }{
				{"priority", `"priority"`, "$0.9120"},
				{"fast", `"priority"`, "$0.9120"},
				{"priority", `"fast"`, "$0.9120"},
				{"priority", `"default"`, "$0.4560"},
				{"default", `"priority"`, "$0.9120"},
				{"auto", `"priority"`, "$0.9120"},
				{"", `"priority"`, "$0.9120"},
				{"priority", "", "$0.9120"},
				{"fast", "", "$0.9120"},
				{"auto", "", "n/a"},
				{"priority", `null`, "n/a"},
				{"priority", `42`, "n/a"},
				{"priority", `""`, "n/a"},
				{"priority", `"unpriced"`, "n/a"},
			} {
				t.Run(fmt.Sprintf("writes=%d/stream=%t/%s/%s", cacheWrites, stream, tc.requested, tc.served), func(t *testing.T) {
					proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
					request := serverRequest(t, func(fields map[string]any) {
						fields["model"], fields["stream"] = "gpt-5.6-sol", stream
						if tc.requested != "" {
							fields["service_tier"] = tc.requested
						}
					})
					headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
					body := map[string]any{
						"id": "tier-response", "status": "completed",
						"output": []any{map[string]any{"id": "answer", "type": "message", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Answer"}}}},
						"usage":  map[string]any{"input_tokens": 100_000, "input_tokens_details": map[string]any{"cached_tokens": 40_000, "cache_write_tokens": cacheWrites}, "output_tokens": 10_000, "output_tokens_details": map[string]any{"reasoning_tokens": 5_000}},
					}
					if tc.served != "" {
						body["service_tier"] = json.RawMessage(tc.served)
					}
					wire := string(mustTestJSON(t, body))
					if stream {
						wire = finalAnswerTestWire(append(finalAnswerTestEvents(t, "final_answer"), mustTestJSON(t, map[string]any{"type": "response.completed", "response": body})))
					}
					response := serverHTTPResponse(wire)
					if stream {
						response.Header.Set("Content-Type", "text/event-stream")
					}
					provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
					var output bytes.Buffer
					if err := executeRequest(t.Context(), t.Context(), request, headers, "tier-session", provider, &output, nil, proxy, nil, nil); err != nil {
						t.Fatal(err)
					}
					want := tc.want
					if cacheWrites != 0 {
						switch want {
						case "$0.9120":
							want = "$0.9520"
						case "$0.4560":
							want = "$0.4760"
						}
						if !strings.Contains(output.String(), "20,000 cache-write tokens") {
							t.Fatalf("lost cache writes: %s", output.String())
						}
					}
					if !strings.Contains(output.String(), "| Total | — | "+want+" |") || !strings.Contains(output.String(), "| Input | 100,000 |") {
						t.Fatalf("report=%s", output.String())
					}
					var forwarded map[string]json.RawMessage
					if json.Unmarshal(provider.forwarded[0], &forwarded) != nil || jsonString(forwarded, "service_tier") != tc.requested {
						t.Fatal("accounting changed requested tier")
					}
				})
			}
		}
	}
}

func TestThreadCostsKeepResponseServiceTiers(t *testing.T) {
	totals := newThreadUsage()
	counts := tokenCounts{InputTokens: 100_000, UncachedInputTokens: 60_000, OutputTokens: 10_000, ReasoningTokens: 5_000}
	totals.observation("thread", "", "gpt-6-astra", "priority").observe(counts)
	counts.ServiceTier = "default"
	totals.observation("thread", "", "gpt-5.6-sol", "fast").observe(counts)
	counts.ServiceTier = "priority"
	totals.observation("thread", "", "gpt-5.6-luna", "default").observe(counts)
	got, ok := totals.snapshot("thread")
	// Astra Fast 2.28 + Sol default .456 + Luna Fast .0496.
	if !ok || !got.cost.known || got.InputTokens != 300_000 || math.Abs(got.cost.uncachedInput+got.cost.cachedInput+got.cost.output-2.7856) > 1e-10 {
		t.Fatalf("report=%+v valid=%t", got, ok)
	}
	counts.ServiceTier = "unpriced"
	totals.observation("thread", "", "gpt-5.6-sol", "default").observe(counts)
	counts.ServiceTier = "default"
	totals.observation("thread", "", "gpt-5.6-sol", "default").observe(counts)
	got, ok = totals.snapshot("thread")
	if !ok || got.cost.known || got.InputTokens != 500_000 {
		t.Fatalf("unpriced tier lost counts or revived partial costs: %+v", got)
	}
}

func TestTokenCostCacheWrites(t *testing.T) {
	for _, tc := range []struct {
		model, tier             string
		input                   uint64
		wantUncached, wantTotal float64
	}{
		{"gpt-6-astra", "default", 200_000, 1.1, 1.7},
		{"gpt-6-astra", "fast", 200_000, 2.2, 3.4},
		{"gpt-6-astra", "priority", 300_000, 4.4, 6.7},
		{"gpt-5.6-sol", "default", 200_000, .44, .68},
		{"gpt-5.6-sol", "priority", 200_000, .88, 1.36},
		{"gpt-5.6-sol", "fast", 300_000, 1.76, 2.68},
		{"gpt-5.6-terra", "priority", 200_000, .44, .72},
		{"gpt-5.6-luna", "fast", 200_000, .044, .072},
	} {
		t.Run(fmt.Sprintf("%s/%s/%d", tc.model, tc.tier, tc.input), func(t *testing.T) {
			counts := tokenCounts{InputTokens: tc.input, UncachedInputTokens: 100_000, CacheWriteTokens: 40_000, OutputTokens: 10_000, ReasoningTokens: 5_000}
			cost := estimateTokenCost(tc.model, tc.tier, counts)
			if !cost.known || math.Abs(cost.uncachedInput-tc.wantUncached) > 1e-10 || math.Abs(cost.uncachedInput+cost.cachedInput+cost.output-tc.wantTotal) > 1e-10 {
				t.Fatalf("cost=%+v want=%+v", cost, tc)
			}
			totals := newThreadUsage()
			for range 2 {
				totals.observation("thread", "", tc.model, tc.tier).observe(counts)
			}
			report, ok := totals.snapshot("thread")
			text := formatTokenUsageReport(report)
			if !ok || report.InputTokens != 2*tc.input || report.CacheWriteTokens != 80_000 || !strings.Contains(text, "80,000 cache-write tokens") {
				t.Fatalf("report=%s", text)
			}
		})
	}
	for _, counts := range []tokenCounts{
		{InputTokens: 10, UncachedInputTokens: 5, CacheWriteTokens: 6},
		{InputTokens: 10, UncachedInputTokens: 5, CacheWriteTokens: 4, Incomplete: true},
	} {
		if estimateTokenCost("gpt-6-astra", "priority", counts).known {
			t.Fatal("priced invalid cache writes")
		}
	}
	if estimateTokenCost("gpt-5.5", "priority", tokenCounts{InputTokens: 10, UncachedInputTokens: 10, CacheWriteTokens: 5}).known {
		t.Fatal("invented unavailable model cache-write price")
	}
}
