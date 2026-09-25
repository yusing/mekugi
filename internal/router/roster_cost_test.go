package router

import (
	"math"
	"testing"
)

func rosterCostTestActivity() *subagentActivity {
	activity := newSubagentActivity()
	activity.usage = newThreadUsage()
	activity.observe("root", "", "/root", false)
	activity.observe("child", "root", "/root/child", true)
	activity.attachPane(&activityPane{root: "root"})
	return activity
}

func rosterCostTestAgent(t *testing.T, activity *subagentActivity, name string) activityPaneAgent {
	t.Helper()
	for _, agent := range activity.paneAgentsLocked() {
		if agent.Name == name {
			return agent
		}
	}
	t.Fatalf("agent %s missing", name)
	return activityPaneAgent{}
}

// The roster displays the canonical per-thread report rather than a parallel total.
func TestRosterUsageMatchesCanonicalReport(t *testing.T) {
	activity := rosterCostTestActivity()
	activity.beginResponse("child")
	activity.streamOutput("child", 400)
	counts := tokenCounts{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000, TotalsKnown: true}
	activity.usage.observation("child", "", "gpt-6-sol", "").observe(counts)
	activity.syncUsage("child")
	activity.endResponse("child")

	report, _ := activity.usage.snapshot("child")
	agent := rosterCostTestAgent(t, activity, "/root/child")
	if !agent.CostKnown || agent.CostPartial || math.Abs(agent.Cost-.3) > 1e-10 ||
		agent.Cost != report.cost.cachedInput+report.cost.uncachedInput+report.cost.output ||
		agent.InputTokens != report.InputTokens || agent.OutputTokens != report.OutputTokens {
		t.Fatalf("roster agent = %+v, canonical report = %+v", agent, report)
	}
	if root := rosterCostTestAgent(t, activity, "/root"); root.CostKnown || root.InputTokens != 0 {
		t.Fatalf("child usage leaked into main: %+v", root)
	}
	if got := liveActivityCost(agent); got != "$0.3000" {
		t.Fatalf("cost = %q", got)
	}
}

// A forwarded response without usable usage leaves the report partial; the
// roster keeps the observed cost as a lower bound instead of hiding it.
func TestRosterUsageGapShowsLowerBoundCost(t *testing.T) {
	activity := rosterCostTestActivity()
	counts := tokenCounts{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000, TotalsKnown: true}
	activity.beginResponse("child")
	activity.usage.observation("child", "", "gpt-6-sol", "").observe(counts)
	activity.endResponse("child")
	activity.beginResponse("child")
	activity.usage.observation("child", "", "gpt-6-sol", "").finish()
	activity.endResponse("child")
	activity.beginResponse("child")
	activity.usage.observation("child", "", "gpt-6-sol", "").observe(counts)
	activity.endResponse("child")

	agent := rosterCostTestAgent(t, activity, "/root/child")
	if !agent.CostKnown || !agent.CostPartial || math.Abs(agent.Cost-.6) > 1e-10 || agent.InputTokens != 200_000 {
		t.Fatalf("gap hid or reset roster usage: %+v", agent)
	}
	if got := liveActivityCost(agent); got != "≥$0.6000" {
		t.Fatalf("cost = %q", got)
	}
}

func TestRosterCostUnknownPricingAndMissingUsage(t *testing.T) {
	activity := rosterCostTestActivity()
	activity.beginResponse("child")
	activity.usage.observation("child", "", "unpriced-model", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1, TotalsKnown: true})
	activity.endResponse("child")
	if agent := rosterCostTestAgent(t, activity, "/root/child"); agent.CostKnown || agent.InputTokens != 10 || liveActivityCost(agent) != "" {
		t.Fatalf("unknown pricing claimed a cost or hid tokens: %+v", agent)
	}
	activity.beginResponse("root")
	activity.endResponse("root")
	if agent := rosterCostTestAgent(t, activity, "/root"); agent.CostKnown || liveActivityCost(agent) != "" {
		t.Fatalf("response without any usage claimed a cost: %+v", agent)
	}
}

func TestRosterCostReportedZeroVersusMissingTotals(t *testing.T) {
	for _, tc := range []struct {
		usage   string
		partial bool
	}{
		{`{"input_tokens":1000,"output_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`, false},
		{`{"input_tokens":0,"output_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`, false},
		{`{"input_tokens":1000}`, true},
		{`{"output_tokens":0}`, true},
	} {
		counts, ok := usageFromResponsePayload([]byte(`{"usage":`+tc.usage+`}`), false)
		if !ok {
			t.Fatal("usage not parsed")
		}
		activity := rosterCostTestActivity()
		activity.beginResponse("child")
		activity.usage.observation("child", "", "gpt-6-sol", "").observe(counts)
		activity.endResponse("child")
		if agent := rosterCostTestAgent(t, activity, "/root/child"); !agent.CostKnown || agent.CostPartial != tc.partial {
			t.Fatalf("%s: agent=%+v", tc.usage, agent)
		}
	}
}
