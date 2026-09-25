package router

import (
	"strings"
	"testing"
	"time"
)

func TestRosterIncludesMainWithoutFilterEvent(t *testing.T) {
	a := newSubagentActivity()
	a.usage = newThreadUsage()
	a.observe("root", "", "/root", false)
	a.observe("child", "root", "/root/child", true)
	a.observe("other", "", "/root", false)
	a.observe("other-child", "other", "/root/other", true)
	a.attachPane(newActivityPane(t.Context(), func() bool { return true }))
	a.pane.root = "root"
	a.beginResponse("root")
	a.streamOutput("root", 40)
	agents := a.paneAgentsLocked()
	if len(agents) != 2 || agents[0].Name != "/root" || agents[1].Name != "/root/child" {
		t.Fatalf("main missing or foreign root leaked: %+v", agents)
	}
	main := agents[0]
	if !main.Responding || main.Turns != 1 || main.OutputTokens != 10 {
		t.Fatalf("main response status missing: %+v", main)
	}
	counts := tokenCounts{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 20}
	a.usage.observation("root", "", "gpt-6-sol", "").observe(counts)
	a.syncUsage("root")
	a.endResponse("root")
	agents = a.paneAgentsLocked()
	main = agents[0]
	if main.Responding || main.LastResponse.IsZero() || main.InputTokens != 100 || main.OutputTokens != 20 || !main.CostKnown {
		t.Fatalf("main final usage/status missing: %+v", main)
	}
	if agents[1].InputTokens != 0 || agents[1].Turns != 0 {
		t.Fatalf("main usage contaminated child: %+v", agents[1])
	}
	v := newLiveActivityView()
	v.apply(activityPaneEvent{Kind: "snapshot", Agents: agents})
	rows := v.roster()
	name, _ := rosterTree(rows, 0, 0)
	if len(rows) != 2 || name != "main" || rows[1].depth != 1 {
		t.Fatalf("main tree label/parent incorrect: %+v", rows)
	}
	frame := strings.Join(plainLines(v.renderRosterPane(80, 8, time.Now())), "\n")
	if !strings.Contains(frame, "main") || !strings.Contains(frame, "child") {
		t.Fatalf("main missing from rendered roster: %s", frame)
	}
	a.usage.observation("root", "", "gpt-6-sol", "").finish()
	if main := a.paneAgentsLocked()[0]; !main.CostKnown || !main.CostPartial {
		t.Fatalf("main usage gap not shown as a lower bound: %+v", main)
	}
}

func TestMainRosterResponseLifecycleAtRequestBoundary(t *testing.T) {
	p := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, p, "session", "thread", "", "/root", nil)
	if !root.activityResponding || p.activity.threads["thread"].responding != 1 {
		t.Fatal("accepted main request did not start roster response tracking")
	}
	counts := tokenCounts{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 20}
	root.observeResponseUsage(counts)
	root.Close()
	node := p.activity.threads["thread"]
	if node.responding != 0 || node.turns != 1 || node.lastResponse.IsZero() {
		t.Fatalf("main request lifecycle not reflected in roster: %+v", node)
	}
	if report, ok := p.activity.usage.snapshot("thread"); !ok || report.InputTokens != 100 || report.OutputTokens != 20 {
		t.Fatalf("main usage not reflected in the roster's usage owner: %+v", report)
	}
	root.Close()
	if node.responding != 0 || node.turns != 1 {
		t.Fatal("repeated close altered main lifecycle")
	}
}
