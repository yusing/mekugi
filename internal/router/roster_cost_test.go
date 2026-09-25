package router

import (
	"math"
	"testing"
)

func TestRosterCostUsesReferencePricesForIncompleteUsage(t *testing.T) {
	counts := tokenCounts{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000, Incomplete: true}
	if authoritative := usageTokenCost("gpt-6-sol", "auto", counts, nil); authoritative.known {
		t.Fatalf("unknown billing detail claimed authoritative cost: %+v", authoritative)
	}
	cost := rosterTokenCost("gpt-6-sol", "auto", counts, nil)
	if !cost.known || math.Abs(cost.uncachedInput+cost.cachedInput+cost.output-.3) > 1e-10 {
		t.Fatalf("roster cost = %+v, want $0.30 estimate", cost)
	}
	if cost := rosterTokenCost("gpt-6-sol", "unknown", counts, nil); cost.known {
		t.Fatalf("unknown tier was priced: %+v", cost)
	}
}

func TestRosterKeepsIncompleteProviderTokensAndEstimatedCost(t *testing.T) {
	activity := newSubagentActivity()
	activity.observe("root", "", "/root", false)
	activity.observe("child", "root", "/root/child", true)
	counts := tokenCounts{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000, Incomplete: true}
	activity.syncUsage("child", counts, tokenUsageReport{}, false, rosterTokenCost("gpt-6-sol", "auto", counts, nil))
	node := activity.threads["child"]
	if node.inputTokens != counts.InputTokens || node.outputTokens != counts.OutputTokens || !node.cost.known ||
		math.Abs(node.cost.uncachedInput+node.cost.cachedInput+node.cost.output-.3) > 1e-10 {
		t.Fatalf("incomplete roster usage = %+v", node)
	}
}

func TestRosterCostReportedZeroVersusMissingTotals(t *testing.T) {
	for _, tc := range []struct {
		usage string
		known bool
	}{
		{`{"input_tokens":1000,"output_tokens":0}`, true},
		{`{"input_tokens":0,"output_tokens":0}`, true},
		{`{"input_tokens":1000}`, false},
		{`{"output_tokens":0}`, false},
	} {
		counts, ok := usageFromResponsePayload([]byte(`{"usage":`+tc.usage+`}`), false)
		if !ok {
			t.Fatal("usage not parsed")
		}
		cost := rosterTokenCost("gpt-6-sol", "auto", counts, nil)
		if cost.known != tc.known {
			t.Fatalf("%s: cost=%+v", tc.usage, cost)
		}
	}
}
