package router

import (
	"math"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestNativeRosterCostMatchesCanonicalUsage(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.proxy = proxy
	counts := tokenCounts{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000, TotalsKnown: true}
	proxy.usage.observation("child", "", "gpt-6-sol", "").observe(counts)
	report, observed := proxy.usage.snapshot("child")
	if !observed {
		t.Fatal("canonical child usage missing")
	}
	agent := activityPaneAgent{Name: "/root/child", Turns: 1, InputTokens: counts.InputTokens, OutputTokens: counts.OutputTokens}
	u.observeCost("child", &agent)
	if !agent.CostKnown || agent.CostPartial || math.Abs(agent.Cost-.3) > 1e-10 ||
		agent.Cost != report.cost.cachedInput+report.cost.uncachedInput+report.cost.output ||
		agent.InputTokens != counts.InputTokens || agent.OutputTokens != counts.OutputTokens {
		t.Fatalf("native agent = %+v, report = %+v", agent, report)
	}
	if got := liveActivityCost(agent); got != "$0.30" {
		t.Fatalf("cost = %q", got)
	}
	root := activityPaneAgent{Name: "/root"}
	u.observeCost("root", &root)
	if root.CostKnown || root.Cost != 0 {
		t.Fatalf("child cost leaked into Main: %+v", root)
	}
}

func TestNativeRosterUsageGapShowsLowerBoundCost(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.proxy = proxy
	counts := tokenCounts{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000, TotalsKnown: true}
	for _, complete := range []bool{true, false, true} {
		observation := proxy.usage.observation("child", "", "gpt-6-sol", "")
		if complete {
			observation.observe(counts)
		} else {
			observation.finish()
		}
	}
	agent := activityPaneAgent{Name: "/root/child", Turns: 3, InputTokens: 200_000}
	u.observeCost("child", &agent)
	if !agent.CostPartial || !agent.CostKnown || math.Abs(agent.Cost-.6) > 1e-10 || agent.InputTokens != 200_000 {
		t.Fatalf("missing usage hid or reset lower bound: %+v", agent)
	}
	if got := liveActivityCost(agent); got != "≥$0.60" {
		t.Fatalf("lower-bound cost = %q", got)
	}
	// T+N counts every forwarded provider request, including one without usage,
	// rather than Codex turns.
	if agent.Roundtrips != 3 || ansi.Strip(liveActivityTurns(agent)) != "T+3" {
		t.Fatalf("roundtrips = %d, label %q", agent.Roundtrips, liveActivityTurns(agent))
	}
}

func TestNativeRosterUnknownAndMissingUsageDoNotClaimCost(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.proxy = proxy
	proxy.usage.observation("child", "", "unpriced-model", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1, TotalsKnown: true})
	for _, thread := range []string{"child", "root"} {
		agent := activityPaneAgent{}
		u.observeCost(thread, &agent)
		if agent.CostKnown || liveActivityCost(agent) != "" {
			t.Fatalf("%s claimed unknown cost: %+v", thread, agent)
		}
	}
}
