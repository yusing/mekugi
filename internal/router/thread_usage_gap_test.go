package router

import (
	"math"
	"strings"
	"testing"
)

func TestThreadUsageGapsPreserveObservedCountsModelsAndCosts(t *testing.T) {
	totals := newThreadUsage()
	first := tokenCounts{InputTokens: 100, UncachedInputTokens: 40, OutputTokens: 10, ReasoningTokens: 3}
	last := tokenCounts{InputTokens: 200, UncachedInputTokens: 50, OutputTokens: 20, ReasoningTokens: 5}
	totals.observation("root", "", "gpt-6-astra", "").observe(first)
	for range 7 {
		gap := totals.observation("root", "", "gpt-6-astra", "")
		gap.finish()
		gap.finish()
		// A repeated terminal on the same request must not erase the gap or count twice.
		gap.observe(first)
	}
	totals.observation("root", "", "gpt-5.6-sol", "").observe(last)
	got, ok := totals.snapshot("root")
	if !ok || got.Incomplete || got.missingUsage != 7 ||
		got.InputTokens != 300 || got.UncachedInputTokens != 90 ||
		got.OutputTokens != 30 || got.ReasoningTokens != 8 ||
		got.model != "gpt-6-astra, gpt-5.6-sol" {
		t.Fatalf("lost observed usage: %+v, ok=%t", got, ok)
	}
	cost := estimateTokenCost("gpt-6-astra", "", first)
	cost.add(estimateTokenCost("gpt-5.6-sol", "", last))
	if !got.cost.known || math.Abs(got.cost.cachedInput-cost.cachedInput) > 1e-12 ||
		math.Abs(got.cost.uncachedInput-cost.uncachedInput) > 1e-12 ||
		math.Abs(got.cost.output-cost.output) > 1e-12 {
		t.Fatalf("cost repriced or included missing responses: %+v want %+v", got.cost, cost)
	}
	text := formatTokenUsageReport(got)
	for _, want := range []string{"| /root (partial) |", "| Total (partial) |", "300 (70.0%)", "| Missing usage |", "| 7 |", "exclude 7 response(s)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report lacks %q:\n%s", want, text)
		}
	}
}

func TestCompletionUsageAggregatesGapsWithoutMutatingThreads(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, proxy, "session", "root", "", "/root", nil)
	child, _ := prepareActivityTest(t, proxy, "session", "child", "root", "/root/child", nil)
	for _, transform := range []*mekugiResponseTransform{root, child} {
		transform.usageTracker.model = "gpt-6-astra"
		transform.observeResponseUsage(tokenCounts{InputTokens: 100, UncachedInputTokens: 20, OutputTokens: 10})
	}
	for range 7 {
		proxy.usage.observation("root", "", "gpt-6-astra", "").finish()
	}
	proxy.usage.observation("child", "", "gpt-6-astra", "").finish()
	for range 2 {
		report, ok := root.completionUsageReport()
		if !ok || report.Incomplete || report.missingUsage != 8 || report.InputTokens != 200 || !report.cost.known {
			t.Fatalf("bad aggregate: %+v, ok=%t", report, ok)
		}
		rows := *report.rows
		if rows[0].report.missingUsage != 7 || rows[1].report.missingUsage != 1 {
			t.Fatalf("gaps crossed thread boundaries: %+v", rows)
		}
	}
	got, _ := proxy.usage.snapshot("root")
	if got.missingUsage != 7 || got.InputTokens != 100 {
		t.Fatalf("rendering mutated root: %+v", got)
	}
}

func TestThreadUsageGapOnlyRetainsModelAndUnknownPricingRemainsUnknown(t *testing.T) {
	totals := newThreadUsage()
	totals.observation("root", "", "gpt-6-astra", "").finish()
	got, ok := totals.snapshot("root")
	if !ok || got.missingUsage != 1 || got.InputTokens != 0 || got.model != "gpt-6-astra" {
		t.Fatalf("gap-only observation lost: %+v, ok=%t", got, ok)
	}
	totals.observation("root", "", "unknown-model", "").observe(tokenCounts{InputTokens: 10})
	got, ok = totals.snapshot("root")
	if !ok || got.InputTokens != 10 || got.missingUsage != 1 || got.cost.known {
		t.Fatalf("missing usage hid counts or invented pricing: %+v, ok=%t", got, ok)
	}
}

func TestCompletionUsageRolesAndGapsFromNativeRequest(t *testing.T) {
	for _, namespace := range []string{"collaboration", "mekugi_collaboration"} {
		t.Run(namespace, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			root, _ := prepareActivityTest(t, proxy, "session", "root", "", "/root", []any{
				map[string]any{"type": "function_call", "namespace": namespace, "name": "spawn_agent", "call_id": "spawn", "arguments": `{"agent_type":"implementer","task_name":"child"}`},
				map[string]any{"type": "function_call_output", "call_id": "spawn", "output": `{"task_name":"/root/child"}`},
			})
			child, _ := prepareActivityTest(t, proxy, "session", "child", "root", "/root/child", nil)
			for _, transform := range []*mekugiResponseTransform{root, child} {
				transform.usageTracker.model = "gpt-6-astra"
				transform.observeResponseUsage(tokenCounts{InputTokens: 100, UncachedInputTokens: 20, OutputTokens: 10})
			}
			proxy.usage.observation("child", "", "gpt-6-astra", "").finish()
			report, ok := root.completionUsageReport()
			text := formatTokenUsageReport(report)
			if !ok || report.Incomplete || report.missingUsage != 1 || report.InputTokens != 200 ||
				!strings.Contains(text, "| /root/child (partial) | implementer | gpt-6-astra | 100 (80.0%) |") ||
				!strings.Contains(text, "| Total (partial) | — | — | 200 (80.0%) |") {
				t.Fatalf("native role and usage did not reach report: %+v, ok=%t\n%s", report, ok, text)
			}
		})
	}
}
