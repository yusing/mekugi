package router

import (
	"math"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func restoredUsageRosterFixture(t *testing.T) *appServerUI {
	t.Helper()
	directory := t.TempDir()
	usage := storedUsageFixture(usageStoreFixture(t, directory))
	usage.observation("child", "child", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000})
	usage.observation("child", "child", "gpt-6-sol", "").finish()
	usage.observation("gap", "gap", "gpt-6-sol", "").finish()
	usage.close()
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.proxy = &mekugiProxy{usage: storedUsageFixture(usageStoreFixture(t, directory))}
	for _, thread := range []string{"child", "gap", "historical"} {
		info := appServerThreadInfo{ID: thread, AgentNickname: thread, Turns: []appServerHistoryTurn{{ID: "old", Status: "completed"}}}
		u.session.registerThread(info)
		u.restoreActivityThread(info)
	}
	return u
}

func TestNativeRosterRestoredUsageUsesProviderTotals(t *testing.T) {
	u := restoredUsageRosterFixture(t)
	child := u.session.agent(u.session.paths["child"])
	if child.Responding || !child.TokensKnown || !child.UsagePartial || child.InputTokens != 100_000 || child.OutputTokens != 10_000 || child.Roundtrips != 2 || !child.CostKnown || !child.CostPartial || math.Abs(child.Cost-.3) > 1e-10 {
		t.Fatalf("idle child usage not restored: %+v", child)
	}
	for _, thread := range []string{"gap", "historical"} {
		agent := u.session.agent(u.session.paths[thread])
		if agent.CostKnown || liveActivityCost(*agent) != "" || agent.InputTokens != 0 || agent.OutputTokens != 0 {
			t.Fatalf("%s invented consumption or zero cost: %+v", thread, agent)
		}
	}
	// Host totals can describe context or replayed tokens, never consumption.
	child.InputTokens, child.OutputTokens = 9_000_000, 8_000_000
	u.observeCost("child", child)
	if child.InputTokens != 100_000 || child.OutputTokens != 10_000 {
		t.Fatalf("host totals overrode provider report: %+v", child)
	}
	u.proxy.usage.observation("child", "child", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000})
	u.observeCost("child", child)
	if child.InputTokens != 200_000 || child.OutputTokens != 20_000 || child.Roundtrips != 3 || !child.CostPartial || math.Abs(child.Cost-.6) > 1e-10 {
		t.Fatalf("followup reset restored child totals: %+v", child)
	}
	unknown := activityPaneAgent{InputTokens: 9_000_000, OutputTokens: 8_000_000, Cost: 99, CostKnown: true}
	u.observeCost("unobserved", &unknown)
	if unknown.InputTokens != 0 || unknown.OutputTokens != 0 || unknown.CostKnown || unknown.Roundtrips != 0 {
		t.Fatalf("unobserved thread borrowed host consumption: %+v", unknown)
	}
	historical := u.session.agent(u.session.paths["historical"])
	u.proxy.usage.observation("historical", "historical", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000})
	u.observeCost("historical", historical)
	if !historical.TokensKnown || !historical.UsagePartial || !historical.CostPartial || historical.Roundtrips != 1 || historical.InputTokens != 100_000 || historical.OutputTokens != 10_000 {
		t.Fatalf("historical followup claimed complete totals: %+v", historical)
	}
	if got := ansi.Strip(liveActivityTurns(*historical)); got != "≥T+1" {
		t.Fatalf("historical roundtrips = %q", got)
	}
}

func TestUISnapshotNativeRosterRestoredUsage(t *testing.T) {
	u := restoredUsageRosterFixture(t)
	u.proxy.usage.observation("historical", "historical", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000})
	u.observeCost("historical", u.session.agent(u.session.paths["historical"]))
	u.agents.apply(activityPaneEvent{Kind: "agents", Agents: u.session.agents})
	u.agents.painter.Theme = livediff.DarkTheme
	u.agents.selected = u.session.paths["child"]
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	assertNativeUISnapshot(t, "native-roster-restored-usage", u.agents.nativeRoster(140, 8, now, true))
}
