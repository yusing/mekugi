package router

import (
	"os"
	"strings"
	"testing"
)

func TestThreadUsageKeepsTypesafeSeparateAndProjectsDescendants(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, proxy, "shared-session", "root", "", "/root", nil)
	child, _ := prepareActivityTest(t, proxy, "shared-session", "child", "root", "/root/worker", nil)
	other, _ := prepareActivityTest(t, proxy, "other-session", "other", "", "/root/other", nil)
	root.usageTracker.model = "gpt-6-astra"
	child.usageTracker.model = "gpt-5.6-sol"
	other.usageTracker.model = "gpt-6-astra"

	rootTokens := tokenCounts{InputTokens: 100, UncachedInputTokens: 60, OutputTokens: 20, ReasoningTokens: 5}
	childTokens := tokenCounts{InputTokens: 40, UncachedInputTokens: 10, OutputTokens: 8, ReasoningTokens: 2}
	root.observeResponseUsage(rootTokens)
	child.observeResponseUsage(childTokens)
	other.observeResponseUsage(tokenCounts{InputTokens: 3, UncachedInputTokens: 3, OutputTokens: 1})

	before, ok := proxy.usage.snapshot("root")
	if !ok {
		t.Fatal("missing root model-usage snapshot")
	}
	proxy.usage.addTypesafe("root", typesafeUsage{InputTokens: 11, OutputTokens: 4, Requests: 2})
	proxy.usage.addTypesafe("child", typesafeUsage{InputTokens: 7, OutputTokens: 3, Requests: 1, MissingResponses: 1})
	proxy.usage.addTypesafe("other", typesafeUsage{InputTokens: 90, OutputTokens: 80, Requests: 5})
	after, ok := proxy.usage.snapshot("root")
	if !ok || after.tokenCounts != before.tokenCounts || after.model != before.model || after.cost != before.cost || after.missingUsage != before.missingUsage || after.Incomplete != before.Incomplete {
		t.Fatalf("TypeSafe accounting changed agent-model usage: before=%+v after=%+v", before, after)
	}
	if after.typesafe != (typesafeUsage{InputTokens: 11, OutputTokens: 4, Requests: 2}) {
		t.Fatalf("root TypeSafe usage = %+v", after.typesafe)
	}

	report, ok := root.completionUsageReport()
	wantTypesafe := typesafeUsage{InputTokens: 18, OutputTokens: 7, Requests: 3, MissingResponses: 1}
	wantTokens := rootTokens
	wantTokens.InputTokens += childTokens.InputTokens
	wantTokens.UncachedInputTokens += childTokens.UncachedInputTokens
	wantTokens.OutputTokens += childTokens.OutputTokens
	wantTokens.ReasoningTokens += childTokens.ReasoningTokens
	wantCost := estimateTokenCost("gpt-6-astra", "", rootTokens)
	wantCost.add(estimateTokenCost("gpt-5.6-sol", "", childTokens))
	if !ok || report.typesafe != wantTypesafe || report.tokenCounts != wantTokens || report.cost != wantCost {
		t.Fatalf("root report leaked other-thread usage or merged provider buckets: %+v", report)
	}

	text := formatTokenUsageReport(report)
	for _, want := range []string{
		"## TypeSafe AI usage",
		"| /root | jev-1.13.0 | 2 | 11 | 4 | 0 | n/a |",
		"| /root/worker | jev-1.13.0 | 1 | 7 | 3 | 1 | n/a |",
		"| Total TypeSafe | jev-1.13.0 | 3 | 18 | 7 | 1 | n/a |",
		"Missing usage counts attempts",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "| /root/other | jev-1.13.0 |") {
		t.Fatalf("unrelated thread appeared in root TypeSafe report:\n%s", text)
	}

}

func TestTokenMetricsFilePersistsTypesafeSection(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	proxy := &mekugiProxy{}
	report := tokenUsageReport{
		InputTokens: 20, UncachedInputTokens: 12, OutputTokens: 6,
		cost:     estimateTokenCost("gpt-6-astra", "", tokenCounts{InputTokens: 20, UncachedInputTokens: 12, OutputTokens: 6}),
		typesafe: typesafeUsage{InputTokens: 13, OutputTokens: 5, Requests: 4, MissingResponses: 1},
	}
	proxy.writeTokenMetrics("stable-thread", report)
	paths := proxy.tokenMetricPaths()
	if len(paths) != 1 {
		t.Fatalf("token metric paths = %q", paths)
	}
	content, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{
		"| Agent | Role | Model |",
		"| /root | main | n/a | 20 (40.0%) |",
		"## TypeSafe AI usage",
		"| Total TypeSafe | jev-1.13.0 | 4 | 13 | 5 | 1 | n/a |",
		"No TypeSafe price is configured",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved token metrics missing %q:\n%s", want, text)
		}
	}
}

func TestThreadTypesafeOverflowStaysIncomplete(t *testing.T) {
	totals := newThreadUsage()
	totals.addTypesafe("root", typesafeUsage{InputTokens: ^uint64(0), Requests: 1})
	totals.addTypesafe("root", typesafeUsage{InputTokens: 1, Requests: 1})
	report, ok := totals.snapshot("root")
	if ok || !report.typesafe.Incomplete {
		t.Fatalf("overflow was not recorded: %+v, observed=%v", report, ok)
	}
	totals.addTypesafe("root", typesafeUsage{InputTokens: 7, Requests: 3})
	continued, _ := totals.snapshot("root")
	if continued.typesafe != report.typesafe {
		t.Fatalf("overflowed accounting resumed: before=%+v after=%+v", report.typesafe, continued.typesafe)
	}
	text := formatTokenUsageReport(report)
	if !strings.Contains(text, "## TypeSafe AI usage") || !strings.Contains(text, "| /root | jev-1.13.0 | n/a | n/a | n/a | n/a | n/a |") ||
		!strings.Contains(text, "Usage totals unavailable due to accounting overflow") || strings.Contains(text, "18446744073709551615") {
		t.Fatalf("overflowed TypeSafe totals were exposed:\n%s", text)
	}
}
