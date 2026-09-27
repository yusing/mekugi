package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestRequestedEditGroupsStayWithinInvocation(t *testing.T) {
	for _, agent := range []string{"Main", "/root/worker"} {
		v := newLiveActivityView()
		v.childrenOnly = agent != "Main"
		text := "Edit `a.go` +2 -1 · python3\n\nCreate `b.go` +3 -0 · python3"
		patch := appServerEditText(appServerItem{Status: "failed", Changes: []appServerFileChange{
			{Path: "c.go", Diff: "+new\n-old\n"},
			{Path: "d.go", Diff: "+new\n"},
		}}, "")
		v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
			{Seq: 1, Agent: agent, Kind: "tool", CallID: "one", Text: text},
			{Seq: 2, Agent: agent, Kind: "tool", CallID: "two", Text: text},
			{Seq: 3, Agent: agent, Kind: "tool", CallID: "patch", Text: patch},
		}})
		feed := v.renderFeed(100, 80)
		if agent == "Main" {
			feed = v.renderConversation(100)
		}
		got := ansi.Strip(strings.Join(feed.lines, "\n"))
		if strings.Count(got, "• python3") != 2 || strings.Count(got, "• apply_patch") != 1 || strings.Contains(got, " · python3") || strings.Contains(got, " · apply_patch") {
			t.Fatalf("%s invocation grouping:\n%s", agent, got)
		}
		for _, want := range []string{"a.go +2 -1", "b.go +3 -0", "c.go", "d.go", "failed"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s lost %q:\n%s", agent, want, got)
			}
		}
	}
}

func TestRequestedEditGroupKeepsUnresolvedPaths(t *testing.T) {
	for _, agent := range []string{"Main", "/root/worker"} {
		for _, known := range []string{"", "Edit `a.go` · python3 (requested)\n\n"} {
			v := newLiveActivityView()
			v.childrenOnly = agent != "Main"
			v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
				{Seq: 1, Agent: agent, Kind: "tool", CallID: "edit", Text: known + "Edit · python3 (requested)\n\nRun `go test ./...`"},
			}})
			feed := v.renderFeed(100, 80)
			if agent == "Main" {
				feed = v.renderConversation(100)
			}
			got := ansi.Strip(strings.Join(feed.lines, "\n"))
			if strings.Count(got, "python3 (requested)") != 1 || !strings.Contains(got, "    Edit   paths unavailable") || !strings.Contains(got, "go test ./...") {
				t.Fatalf("%s lost or detached unresolved targets:\n%s", agent, got)
			}
			if known != "" && !strings.Contains(got, "a.go") {
				t.Fatalf("%s lost the known target:\n%s", agent, got)
			}
		}
	}
}

func TestRequestedReasoningMergedWithOperationGroup(t *testing.T) {
	for _, agent := range []string{"Main", "/root/worker"} {
		for _, operation := range []struct{ text, header string }{
			{"Read `a.go`\n\nSearch `needle`", "Explored"},
			{"Edit `a.go` +2 -1 · git stash push\n\nEdit `b.go` +1 -0 · git stash push", "git stash push"},
		} {
			v := newLiveActivityView()
			v.childrenOnly = agent != "Main"
			v.conversation = agent == "Main"
			v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
				{Seq: 1, Agent: agent, Kind: "reasoning", Text: "**Locating targeted files**"},
				{Seq: 2, Agent: agent, Kind: "tool", CallID: "op", Text: operation.text},
			}})
			feed := v.renderFeed(100, 80)
			if agent == "Main" {
				feed = v.renderConversation(100)
			}
			got := ansi.Strip(strings.Join(feed.lines, "\n"))
			if !strings.Contains(got, "• "+operation.header+" · Locating targeted files") || strings.Count(got, "Locating targeted files") != 1 {
				t.Fatalf("%s did not merge corresponding reasoning:\n%s", agent, got)
			}
		}
	}
}

func TestRequestedExploredKeepsAttachedFilter(t *testing.T) {
	for _, agent := range []string{"Main", "/root/worker"} {
		v := newLiveActivityView()
		v.childrenOnly = agent != "Main"
		v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
			{Seq: 1, Agent: agent, Kind: "tool", CallID: "read", Text: "Read `a.go`"},
			{Seq: 2, Agent: agent, Kind: "output_filter", CallID: "read", Text: "filtered output", Filter: &exploreFilterEvent{}},
			{Seq: 3, Agent: agent, Kind: "tool", CallID: "search", Text: "Search `needle`"},
		}})
		feed := v.renderFeed(100, 80)
		if agent == "Main" {
			feed = v.renderConversation(100)
		}
		got := ansi.Strip(strings.Join(feed.lines, "\n"))
		if strings.Count(got, "• Explored") != 1 || !strings.Contains(got, "    filtered output") || strings.Contains(got, "└ filtered") || strings.Contains(got, "├ filtered") || !strings.Contains(got, "Search") {
			t.Fatalf("%s detached filter from exploration:\n%s", agent, got)
		}
	}
}

func TestRequestedInspectOperandsGroupLikeReads(t *testing.T) {
	got := toolActivityShell("inspect_file --json --max-tokens 500 a.go b.go")
	blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: got})
	if len(blocks) != 1 || blocks[0].Kind != "reads" || blocks[0].Verb != "Inspect" ||
		len(blocks[0].Reads) != 2 || blocks[0].Reads[0].Path != "a.go" || blocks[0].Reads[1].Path != "b.go" {
		t.Fatalf("inspect operands were not one grouped operation: %q, %+v", got, blocks)
	}
}

func TestRequestedConsecutiveInspectGroupingBoundaries(t *testing.T) {
	now := time.Now()
	view := newLiveActivityView()
	view.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{{Name: "/root/a"}, {Name: "/root/b"}}, Entries: []activityPaneEntry{
		{Seq: 1, Agent: "/root/a", Kind: "tool", Text: "Inspect `a.go`\n\nInspect `b.go`", Observed: now},
		{Seq: 2, Agent: "/root/a", Kind: "tool", Text: "Inspect `c.go`", Observed: now},
		{Seq: 3, Agent: "/root/a", Kind: "tool", Text: "Read `d.go`", Observed: now},
		{Seq: 4, Agent: "/root/a", Kind: "tool", Text: "Edit `e.go` +1 -1\n```diff\n-old\n+new\n```", Observed: now},
		{Seq: 5, Agent: "/root/a", Kind: "tool", Text: "Inspect `f.go`", Observed: now},
		{Seq: 6, Agent: "/root/b", Kind: "tool", Text: "Inspect `g.go`", Observed: now},
	}})
	feed := strings.Join(plainLines(view.renderFeed(150, 28).lines), "\n")
	if strings.Count(feed, "Inspect ") != 3 || !strings.Contains(feed, "a.go · b.go · c.go") ||
		!strings.Contains(feed, "Read ") || !strings.Contains(feed, "Edit ") ||
		!strings.Contains(feed, "f.go") || !strings.Contains(feed, "g.go") {
		t.Fatalf("inspect grouping crossed action, detail, or agent boundary:\n%s", feed)
	}
}

func TestRequestedRosterGlyphCenteredInThreeCellGutter(t *testing.T) {
	now := time.Now()
	view := liveActivityTestView("/root", "/root/a")
	view.agents[1].Responding = true
	lines := plainLines(view.render(100, 16, now))
	for _, glyph := range []string{"·", "◐"} {
		found := false
		for _, line := range lines {
			if strings.HasPrefix(line, " "+glyph+" ") {
				found = true
			}
		}
		if !found {
			t.Fatalf("glyph %q not centered in three-cell gutter: %q", glyph, lines)
		}
	}
}

func TestRequestedRootRosterOwnActivityAndAddressedMessage(t *testing.T) {
	now := time.Now()
	view := newLiveActivityView()
	view.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{{Name: "/root"}, {Name: "/root/a"}}, Entries: []activityPaneEntry{
		{Seq: 1, Agent: "/root", Kind: "tool", Text: "Read `main.go`", Observed: now},
		{Seq: 2, Agent: "/root/a", Kind: "tool", Text: "Read `child.go`", Observed: now},
	}})
	if summary, _ := view.current(view.agents[0], now); !strings.Contains(summary, "main.go") {
		t.Fatalf("root own activity missing: %q", summary)
	}
	view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 3, Agent: "/root/a", Kind: "reply", Text: "[`/root/a` -> `/root`] Message received:\nAnswer", Observed: now}}})
	if summary, _ := view.current(view.agents[0], now); !strings.Contains(ansi.Strip(summary), "← a") {
		t.Fatalf("newer addressed message missing: %q", summary)
	}
}

func TestRequestedPaneLaunchesOnResponseBegin(t *testing.T) {
	f := newActivityPaneFixture(t, true)
	if f.launches != 0 {
		t.Fatalf("pane launched before response: %d", f.launches)
	}
	f.activity.beginResponse("explorer")
	if f.launches != 1 {
		t.Fatalf("pane did not launch at response begin, before tools: %d", f.launches)
	}
	f.activity.collect("explorer", "tool", "tool", "Read `x.go`")
	if f.launches != 1 {
		t.Fatalf("tool relaunched pane: %d", f.launches)
	}
}

func TestRequestedRosterMetricSlotsStableAcrossAvailabilityAndFormat(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	view := liveActivityTestView("/root/a")
	view.agents[0].Started = now.Add(-52 * time.Second)
	view.agents[0].LastResponse = now
	view.agents[0].InputTokens, view.agents[0].OutputTokens = 140_600, 789
	view.agents[0].Turns, view.agents[0].Cost, view.agents[0].CostKnown = 7, .5171, true
	full := plainLines(view.metricTable(view.roster(), now))[0]
	view.agents[0].InputTokens, view.agents[0].OutputTokens = 73_700, 844
	view.agents[0].Turns, view.agents[0].Cost = 14, 12.0811
	view.agents[0].LastResponse = now.Add(-4 * time.Second)
	changed := plainLines(view.metricTable(view.roster(), now))[0]
	view.agents[0].InputTokens, view.agents[0].OutputTokens = 0, 0
	view.agents[0].Turns, view.agents[0].CostKnown = 0, false
	empty := plainLines(view.metricTable(view.roster(), now))[0]
	for _, marker := range []string{"·", "↑", "↓", "$", "T+"} {
		a, b := strings.Index(full, marker), strings.Index(changed, marker)
		if a < 0 || b < 0 || ansi.StringWidth(full[:a]) != ansi.StringWidth(changed[:b]) {
			t.Fatalf("metric %q shifted after value format change: %q / %q", marker, full, changed)
		}
	}
	if ansi.StringWidth(full) != ansi.StringWidth(changed) || ansi.StringWidth(full) != ansi.StringWidth(empty) {
		t.Fatalf("metric slots changed width with unavailable values: %q / %q / %q", full, changed, empty)
	}
	if ansi.StringWidth(full) > 46 || !strings.Contains(full, "$0.52") || !strings.Contains(full, "T+7") {
		t.Fatalf("metrics waste space or lost compact cost/turn formatting: %q", full)
	}
}
