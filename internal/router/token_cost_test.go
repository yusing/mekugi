package router

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

const testTokenUsageTable = "Tokens:\n\n| Category | Tokens | API USD |\n| --- | ---: | ---: |\n" +
	"| Input | 20 | — |\n| Cached input | 12 | n/a |\n| Uncached input | 8 | n/a |\n| Output | 5 | n/a |\n| Reasoning | 3 | — |\n| Total | — | n/a |\n"

func TestTokenCostDisjointCategories(t *testing.T) {
	counts := tokenCounts{InputTokens: 100_000, UncachedInputTokens: 40_000, OutputTokens: 30_000, ReasoningTokens: 20_000}
	for _, model := range []string{"gpt-6-astra", "openai/gpt-6-astra"} {
		cost := estimateTokenCost(model, "", counts)
		if !cost.known || cost.uncachedInput != 0.4 || cost.cachedInput != 0.06 || cost.output != 1.5 {
			t.Fatalf("%s: %+v", model, cost)
		}
	}
	for _, model := range []string{"", "unknown", "grok:grok-4.6", "other/gpt-6-astra", "gpt-6-astra-unknown"} {
		if estimateTokenCost(model, "", counts).known {
			t.Fatalf("invented price for %q", model)
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
	want := "Tokens:\n\n| Category | Tokens | API USD |\n| --- | ---: | ---: |\n" +
		"| Input | 100,000 | — |\n| Cached input | 60,000 | $0.0600 |\n| Uncached input | 40,000 | $0.4000 |\n" +
		"| Output | 30,000 | $1.5000 |\n| Reasoning | 20,000 | — |\n| Total | — | $1.9600 |\n"
	if !strings.HasPrefix(got, want) || !strings.Contains(got, "not subscription charges") || strings.Count(got, "\n") != 11 {
		t.Fatalf("report:\n%s", got)
	}
	report.cost.known = false
	got = formatTokenUsageReport(report)
	if strings.Contains(got, "$") || strings.Count(got, "| n/a |") != 4 ||
		!strings.Contains(got, "| Input | 100,000 |") || !strings.Contains(got, "Cost unavailable:") {
		t.Fatalf("unavailable report:\n%s", got)
	}
}

func TestFormatUsageTokens(t *testing.T) {
	for value, want := range map[uint64]string{0: "0", 12: "12", 123: "123", 1234: "1,234", 1234567: "1,234,567", ^uint64(0): "18,446,744,073,709,551,615"} {
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
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
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
				if err := executeRequest(t.Context(), t.Context(), request, headers, model, provider, &output, nil, proxy, nil, nil); err != nil {
					t.Fatal(err)
				}
				if step == 0 {
					if output.String() != wire {
						t.Fatal("compaction output changed")
					}
					continue
				}
				wantCost := "$2.2400"
				if tc.serviceTier != "" {
					wantCost = "$4.4800"
				}
				if tc.invalid != "" {
					wantCost = "n/a"
					if strings.Count(output.String(), "| n/a |") != 4 || !strings.Contains(output.String(), "Cost unavailable:") {
						t.Fatal("inconsistent provider usage was priced", output.String())
					}
				}
				for _, want := range []string{"| Input | 400,000 |", "| Total | — | " + wantCost + " |", "No files were changed."} {
					if !strings.Contains(output.String(), want) {
						t.Fatalf("missing %q from %s", want, output.String())
					}
				}
				if strings.Count(output.String(), "| Category | Tokens | API USD |") != 1 {
					t.Fatal("cost report duplicated", output.String())
				}
			}
		})
	}
}
