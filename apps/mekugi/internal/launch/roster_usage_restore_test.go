package launch

import (
	"math"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/libraries/diffrender"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func restoredUsageRosterFixture(t *testing.T) *appServerUI {
	t.Helper()
	directory := t.TempDir()
	usage := storedUsageFixture(t, usageStoreFixture(t, directory))
	usage.Observation("child", "child", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000})
	usage.Observation("child", "child", "gpt-6-sol", "").Finish()
	usage.Observation("gap", "gap", "gpt-6-sol", "").Finish()
	usage.Close()
	u := newAppServerSessionTestUI(t, t.TempDir())
	setTestAppServerIntegration(u, &observationFixture{usage: storedUsageFixture(t, usageStoreFixture(t, directory))})

	for _, thread := range []string{"child", "gap", "historical"} {
		info := binding.ThreadInfo{ID: thread, AgentNickname: thread, Turns: []binding.HistoryTurn{{ID: "old", Status: "completed"}}}
		registerThread(&u.attached.session, info)
		restoreActivityThread(&u.attached, u.shell, u.clock, &u.restoring, u.ctx, u.review, &u.pendingEditReceipts, u.retained, u.retainedSession, u.activity, u.usage, u.segments, &u.restoredSegments, info, false)
	}
	return u
}

func TestNativeRosterRestoredUsageUsesProviderTotals(t *testing.T) {
	u := restoredUsageRosterFixture(t)
	child := u.attached.session.Agent(u.attached.session.KnownCaller("child"))
	if child.Responding || !child.TokensKnown || !child.UsagePartial || child.InputTokens != 100_000 || child.OutputTokens != 10_000 || child.Roundtrips != 2 || !child.CostKnown || !child.CostPartial || math.Abs(child.Cost-.3) > 1e-10 {
		t.Fatalf("idle child usage not restored: %+v", child)
	}
	for _, thread := range []string{"gap", "historical"} {
		agent := u.attached.session.Agent(u.attached.session.KnownCaller(thread))
		if agent.CostKnown || activityui.Cost(testActivityAgent(*agent)) != "" || agent.InputTokens != 0 || agent.OutputTokens != 0 {
			t.Fatalf("%s invented consumption or zero cost: %+v", thread, agent)
		}
	}
	// Host totals can describe context or replayed tokens, never consumption.
	child.InputTokens, child.OutputTokens = 9_000_000, 8_000_000
	observeCost(u.usage, "child", child)
	if child.InputTokens != 100_000 || child.OutputTokens != 10_000 {
		t.Fatalf("host totals overrode provider report: %+v", child)
	}
	u.usage.Observation("child", "child", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000})
	observeCost(u.usage, "child", child)
	if child.InputTokens != 200_000 || child.OutputTokens != 20_000 || child.Roundtrips != 3 || !child.CostPartial || math.Abs(child.Cost-.6) > 1e-10 {
		t.Fatalf("followup reset restored child totals: %+v", child)
	}
	unknown := sessionpresentation.ActivityAgent{InputTokens: 9_000_000, OutputTokens: 8_000_000, Cost: 99, CostKnown: true}
	observeCost(u.usage, "unobserved", &unknown)
	if unknown.InputTokens != 0 || unknown.OutputTokens != 0 || unknown.CostKnown || unknown.Roundtrips != 0 {
		t.Fatalf("unobserved thread borrowed host consumption: %+v", unknown)
	}
	historical := u.attached.session.Agent(u.attached.session.KnownCaller("historical"))
	u.usage.Observation("historical", "historical", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000})
	observeCost(u.usage, "historical", historical)
	if !historical.TokensKnown || !historical.UsagePartial || !historical.CostPartial || historical.Roundtrips != 1 || historical.InputTokens != 100_000 || historical.OutputTokens != 10_000 {
		t.Fatalf("historical followup claimed complete totals: %+v", historical)
	}
	if got := ansi.Strip(activityui.Turns(testActivityAgent(*historical))); got != "≥T+1" {
		t.Fatalf("historical roundtrips = %q", got)
	}
}

func TestUISnapshotNativeRosterRestoredUsage(t *testing.T) {
	u := restoredUsageRosterFixture(t)
	u.usage.Observation("historical", "historical", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000})
	observeCost(u.usage, "historical", u.attached.session.Agent(u.attached.session.KnownCaller("historical")))
	u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Agents: u.attached.session.Roster()})
	u.shell.Agents.Painter.Theme = diffrender.DarkTheme
	u.shell.Agents.Selected = u.attached.session.KnownCaller("child")
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	assertNativeUISnapshot(t, "native-roster-restored-usage", u.testActivity(u.shell.Agents).NativeRoster(140, 8, now, true))
}
