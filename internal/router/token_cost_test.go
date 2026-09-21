package router

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

const testTokenUsageTable = "Tokens for this session\n\n| Agent | Role | Model | Input (cache hit) | Cache write | Output | Reasoning | Input cost (cached + uncached) | Output cost | Total cost | Missing usage |\n"

func TestTokenCostDisjointCategories(t *testing.T) {
	counts := tokenCounts{InputTokens: 100_000, UncachedInputTokens: 40_000, OutputTokens: 30_000, ReasoningTokens: 20_000}
	for _, model := range []string{"gpt-6-astra", "openai/gpt-6-astra"} {
		cost := estimateTokenCost(model, "", counts)
		if !cost.known || cost.uncachedInput != 0.4 || cost.cachedInput != 0.06 || cost.output != 1.5 {
			t.Fatalf("%s: %+v", model, cost)
		}
	}
	for _, model := range []string{"", "unknown", "grok:unknown", "other/gpt-6-astra", "gpt-6-astra-unknown", "x-ai/grok-4.6"} {
		if estimateTokenCost(model, "", counts).known {
			t.Fatalf("invented price for %q", model)
		}
	}
	for _, model := range []string{"grok:grok-4.6", "grok-4.6", "GROK:GROK-4.6"} {
		cost := estimateTokenCost(model, "", counts)
		if !cost.known || cost.uncachedInput != 0.08 || cost.cachedInput != 0.03 || cost.output != 0.18 {
			t.Fatalf("%s: %+v", model, cost)
		}
	}
	if estimateTokenCost("gpt-6-astra", "", tokenCounts{InputTokens: 1, UncachedInputTokens: 2}).known {
		t.Fatal("priced inconsistent input")
	}
	if cost := estimateTokenCost("gpt-6-astra", "", tokenCounts{}); !cost.known || cost.output != 0 {
		t.Fatal("zero usage must have a known zero estimate", cost)
	}
}

func TestTokenCostContextTierBoundary(t *testing.T) {
	for _, tc := range []struct {
		model                             string
		input                             uint64
		wantInput, wantCached, wantOutput float64
	}{
		{"gpt-6-astra", 271_999, 1, 0.171999, 0.5},
		{"gpt-6-astra", 272_000, 1, 0.172, 0.5},
		{"gpt-6-astra", 272_001, 2, 0.344002, 0.75},
		{"gpt-5.4-mini", 272_000, 0.075, 0.0129, 0.045},
	} {
		counts := tokenCounts{InputTokens: tc.input, UncachedInputTokens: 100_000, OutputTokens: 10_000}
		cost := estimateTokenCost(tc.model, "", counts)
		if !cost.known || math.Abs(cost.uncachedInput-tc.wantInput) > 1e-10 ||
			math.Abs(cost.cachedInput-tc.wantCached) > 1e-10 || math.Abs(cost.output-tc.wantOutput) > 1e-10 {
			t.Fatalf("%s input=%d: %+v", tc.model, tc.input, cost)
		}
	}
}

func TestTokenCostGrokContextTierAndUnsupportedCombinations(t *testing.T) {
	for _, tc := range []struct {
		model, tier                       string
		input                             uint64
		writes                            uint64
		wantInput, wantCached, wantOutput float64
		known                             bool
	}{
		{"grok:grok-4.6", "", 199_999, 0, 0.2, 0.0499995, 0.06, true},
		{"grok-4.6", "default", 200_000, 0, 0.4, 0.1, 0.12, true},
		{"grok:grok-4.6", "", 200_001, 0, 0.4, 0.100001, 0.12, true},
		{"grok:grok-4.6", "fast", 100_000, 0, 0, 0, 0, false},
		{"grok:grok-4.6", "priority", 200_000, 0, 0, 0, 0, false},
		{"grok:grok-4.6", "", 100_000, 1, 0, 0, 0, false},
	} {
		counts := tokenCounts{InputTokens: tc.input, UncachedInputTokens: 100_000, CacheWriteTokens: tc.writes, OutputTokens: 10_000, ReasoningTokens: 5_000}
		got := estimateTokenCost(tc.model, tc.tier, counts)
		if got.known != tc.known || math.Abs(got.uncachedInput-tc.wantInput) > 1e-10 || math.Abs(got.cachedInput-tc.wantCached) > 1e-10 || math.Abs(got.output-tc.wantOutput) > 1e-10 {
			t.Fatalf("%s/%s/%d: %+v", tc.model, tc.tier, tc.input, got)
		}
	}
}

func TestThreadCostsKeepPerResponseModelsAndTiers(t *testing.T) {
	totals := newThreadUsage()
	counts := tokenCounts{InputTokens: 200_000, UncachedInputTokens: 100_000, OutputTokens: 10_000}
	first := totals.observation("root", "", "gpt-6-astra", "")
	first.observe(counts)
	first.observe(counts)
	totals.observation("child", "", "gpt-6-astra", "").observe(counts)
	totals.observation("root", "", "gpt-5.6-sol", "").observe(counts)
	report, ok := totals.snapshot("root")
	if !ok || report.InputTokens != 400_000 || !report.cost.known {
		t.Fatal("lost cumulative usage or duplicate observation counted", report, ok)
	}
	if got := report.cost.uncachedInput + report.cost.cachedInput + report.cost.output; math.Abs(got-2.24) > 1e-10 {
		t.Fatal("repriced history, combined context tiers, or included child usage", got)
	}
	// Once a response is unpriced, later priced usage cannot restore completeness.
	totals.observation("root", "", "unknown", "").observe(counts)
	totals.observation("root", "", "gpt-6-astra", "").observe(counts)
	report, ok = totals.snapshot("root")
	if !ok || report.InputTokens != 800_000 || report.cost.known {
		t.Fatal("partial cost presented as complete or token counts lost", report, ok)
	}
}

func TestTokenUsageReportTables(t *testing.T) {
	counts := tokenCounts{InputTokens: 100_000, UncachedInputTokens: 40_000, OutputTokens: 30_000, ReasoningTokens: 20_000}
	report := tokenUsageReport{tokenCounts: counts, cost: estimateTokenCost("gpt-6-astra", "", counts)}
	got := formatTokenUsageReport(report)
	want := "| /root | main | n/a | 100K (60.0%) | 0 | 30K | 20K | $0.0600+$0.4000=$0.4600 | $1.5000 | $1.9600 |"
	if !strings.Contains(got, want) {
		t.Fatalf("report:\n%s", got)
	}
	report.cost.known = false
	got = formatTokenUsageReport(report)
	if strings.Contains(got, "$") || !strings.Contains(got, "| n/a | n/a | n/a |") ||
		!strings.Contains(got, "100K (60.0%)") || !strings.Contains(got, "Cost unavailable:") {
		t.Fatalf("unavailable report:\n%s", got)
	}
}

func TestFormatUsageTokens(t *testing.T) {
	for value, want := range map[uint64]string{0: "0", 12: "12", 123: "123", 1234: "1.2K", 149000: "149K", 1234567: "1.2M", 1200000000: "1.2B", ^uint64(0): "18446744073.7B"} {
		if got := formatUsageTokens(value); got != want {
			t.Errorf("%d: %q != %q", value, got, want)
		}
	}
}

func TestTokenCostReportIncludesCompactionAcrossTransports(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stream      bool
		invalid     string
		serviceTier string
	}{
		{"json", false, "", ""},
		{"sse", true, "", ""},
		{"json-priority", false, "", "priority"},
		{"sse-fast", true, "", "fast"},
		{"json-excess-cache", false, "cache", ""},
		{"sse-excess-cache", true, "cache", ""},
		{"json-excess-reasoning", false, "reasoning", ""},
		{"sse-excess-reasoning", true, "reasoning", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			for step, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
				requestStream := tc.stream || step == 0 // Compaction requires streaming.
				request := serverRequest(t, func(fields map[string]any) {
					fields["model"], fields["stream"] = model, requestStream
					if tc.serviceTier != "" {
						fields["service_tier"] = tc.serviceTier
					}
					if step == 0 {
						delete(fields, "tools")
						fields["input"] = []any{map[string]any{"role": "user", "content": "compact"}}
						fields["parallel_tool_calls"] = false
					}
				})
				headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
				if step == 0 {
					headers = serverCompactionMetadataHeaders(t)
					headers.Set(threadIDHeader, "thread-1")
				}
				body := map[string]any{
					"id": model, "status": "completed", "output": []any{},
					"usage": map[string]any{
						"input_tokens": 200_000, "input_tokens_details": map[string]any{"cached_tokens": 100_000},
						"output_tokens": 10_000, "output_tokens_details": map[string]any{"reasoning_tokens": 5_000},
					},
				}
				if step == 1 {
					usage := body["usage"].(map[string]any)
					switch tc.invalid {
					case "cache":
						usage["input_tokens_details"] = map[string]any{"cached_tokens": 300_000}
					case "reasoning":
						usage["output_tokens_details"] = map[string]any{"reasoning_tokens": 20_000}
					}
				}

				if step == 1 && !requestStream {
					body["output"] = []any{map[string]any{
						"type": "message", "id": "answer", "role": "assistant", "phase": "final_answer", "status": "completed",
						"content": []any{map[string]any{"type": "output_text", "text": "No files were changed."}},
					}}
				}
				wire := string(mustTestJSON(t, body))
				if requestStream {
					var events [][]byte
					if step == 1 {
						events = finalAnswerTestEvents(t, "final_answer")
					}
					events = append(events, mustTestJSON(t, map[string]any{"type": "response.completed", "response": body}))
					wire = finalAnswerTestWire(events)
				}
				response := serverHTTPResponse(wire)
				if requestStream {
					response.Header.Set("Content-Type", "text/event-stream")
				}
				provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
				var output bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), request, headers, model, provider, &output, nil, proxy, nil); err != nil {
					t.Fatal(err)
				}
				if step == 0 {
					if output.String() != wire {
						t.Fatal("compaction output changed")
					}
					continue
				}
				rendered := output.String()
				if requestStream {
					var notices []string
					for _, payload := range finalAnswerTestPayloads(rendered) {
						var event map[string]json.RawMessage
						if err := json.Unmarshal(payload, &event); err != nil {
							t.Fatal(err)
						}
						if jsonString(event, "type") == "response.output_item.done" {
							notices = append(notices, string(payload))
						}
					}
					rendered = strings.Join(notices, "\n")
				}
				wantCost := "$2.2400"
				if tc.serviceTier != "" {
					wantCost = "$4.4800"
				}
				if tc.invalid != "" {
					wantCost = "n/a"
					if strings.Contains(rendered, "$") || !strings.Contains(rendered, "Cost unavailable:") {
						t.Fatal("inconsistent provider usage was priced", rendered)
					}
				}
				for _, want := range []string{"400K (", wantCost + " |"} {
					if !strings.Contains(rendered, want) {
						t.Fatalf("missing %q from %s", want, rendered)
					}
				}
				if !strings.Contains(output.String(), "No files were changed.") {
					t.Fatal("provider final text was filtered")
				}
				if strings.Count(rendered, "| Agent | Role | Model |") != 1 {
					t.Fatal("cost report duplicated", rendered)
				}
			}
		})
	}
}
