package router

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestRosterDesignMessageOwnerAndAssignment(t *testing.T) {
	p := activityui.Painter{}
	for _, tc := range []struct{ owner, from, to, want string }{
		{"/root/a", "/root/a", "/root", "→ main"},
		{"/root", "/root/a", "/root", "← a"},
		{"/root/a", "/root/a", "/root/b", "→ b"},
		{"/root/b", "/root/a", "/root/b", "← a"},
		{"/root/a/x", "/root/a/x", "/root/a", "→ a"},
		{"/root/a", "/root/a/x", "/root/a", "← a/x"},
	} {
		blocks := parseLiveActivity(sessionpresentation.ActivityEntry{Agent: tc.owner, Kind: "reply", Message: &sessionpresentation.ActivityMessage{From: tc.from, To: tc.to, Text: "Message body"}})
		if len(blocks) != 1 || blocks[0].Kind != "message" {
			t.Fatalf("parsed message = %+v", blocks)
		}
		for _, got := range []string{ansi.Strip(strings.Join(p.Block(blocks[0], 80), "\n")), ansi.Strip(p.Summary(blocks, 80))} {
			if !strings.Contains(got, tc.want) || !strings.Contains(got, "Message body") {
				t.Errorf("%s -> %s owned by %s: %q, want direction %q and body", tc.from, tc.to, tc.owner, got, tc.want)
			}
		}
	}
	now := time.Now()
	v := liveActivityTestView("/root", "/root/a")
	if empty, _ := v.testCurrent(v.Agents[0], now); ansi.Strip(empty) != "Read root.go" {
		t.Errorf("main without inbound = %q", empty)
	}
	v.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{
		{Seq: 3, Agent: "/root/a", Kind: "reply", Message: &sessionpresentation.ActivityMessage{From: "/root/a", To: "/root", Text: "Older inbound"}, Observed: now},
		{Seq: 4, Agent: "/root/a", Kind: "reply", Message: &sessionpresentation.ActivityMessage{From: "/root/a", To: "/root", Text: "Latest inbound"}, Observed: now},
	}})
	summary, _ := v.testCurrent(v.Agents[0], now)
	if !strings.Contains(ansi.Strip(summary), "Latest inbound") || strings.Contains(ansi.Strip(summary), "Older inbound") || !strings.Contains(ansi.Strip(summary), "← a") {
		t.Fatalf("main inbound summary = %q", summary)
	}
}

func TestRosterDesignSeparateHeaderTreeAndMetrics(t *testing.T) {
	now := time.Now()
	v := newActivityAdapter()
	v.Apply(sessionpresentation.ActivityEvent{Kind: "snapshot", Agents: []sessionpresentation.ActivityAgent{
		{Name: "/root", Role: "root-role"},
		{Name: "/root/a", Role: "explorer", Turns: 1, Roundtrips: 1},
		{Name: "/root/b", Responding: true},
		{Name: "/root/a/x"}, {Name: "/root/a/y"},
	}, Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "/root/a/x", Kind: "error", Text: "failed", Observed: now}}})
	rows := v.testRoster()
	var names []string
	for _, row := range rows {
		names = append(names, row.Agent.Name)
	}
	if got := strings.Join(names, ","); got != "/root,/root/a,/root/a/x,/root/a/y,/root/b" {
		t.Fatalf("tree order = %s", got)
	}
	for _, tc := range []struct {
		i    int
		want string
	}{{2, "├ x"}, {3, "└ y"}} {
		name, _ := activityui.RosterTree(rows, tc.i, 0)
		if !strings.Contains(name, tc.want) {
			t.Errorf("tree %d = %q", tc.i, name)
		}
	}
	name, _ := activityui.RosterTree(rows, 3, 3)
	if name != "a/y" {
		t.Errorf("offscreen parent name = %q", name)
	}
	v.StatusLabel = ""
	roster := strings.Join(plainLines(v.renderRosterPane(90, 12, now)), "\n")
	if !strings.Contains(roster, "5 agents · 1 responding · 1 error") || strings.Contains(roster, "FOLLOW") || strings.Contains(roster, "ACTIVITY") {
		t.Errorf("roster header = %q", roster)
	}
	feed := strings.Join(plainLines(v.Render(90, 20, now)), "\n")
	if !strings.Contains(feed, "FOLLOW") || !strings.Contains(feed, "a only") {
		t.Errorf("feed chrome = %q", feed)
	}
	v.FeedOnly = true
	feedOnly := strings.Join(plainLines(v.Render(90, 20, now)), "\n")
	if !strings.Contains(feedOnly, "ACTIVITY") || strings.Contains(feedOnly, "click agent") {
		t.Errorf("feed-only chrome = %q", feedOnly)
	}
	strip := ansi.Strip(v.RenderStrip(rows, 90))
	if !strings.Contains(strip, "main") || strings.Contains(strip, "/root") {
		t.Errorf("strip = %q", strip)
	}
	v.Hovered = "/root/a"
	if hovered := v.RenderStrip(rows, 90); !strings.Contains(hovered, "\x1b[4ma\x1b[24m") {
		t.Errorf("strip hover styling = %q", hovered)
	}
	table := v.MetricTable(rows[:2], now)
	if metric := table[1]; strings.Contains(metric, "explorer") || !strings.Contains(metric, terminalui.Dim+"T+"+terminalui.Undim+"1") || strings.Contains(metric, "n/a") {
		t.Errorf("metric style = %q", metric)
	}
	if strings.Contains(table[0], "root-role") {
		t.Error("root role leaked into metrics")
	}
}

func TestRosterDesignCompactViewportAndWheel(t *testing.T) {
	now := time.Now()
	v := liveActivityTestView("/root/a", "/root/b", "/root/c", "/root/d", "/root/e", "/root/f", "/root/g", "/root/h")
	v.Agents[0].Responding = true
	v.Agents[7].Responding = true
	// A responding agent's earlier error counts once, as its visible ◐ status.
	v.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{
		{Seq: 9, Agent: "/root/g", Kind: "error", Text: "failed", Observed: now},
		{Seq: 10, Agent: "/root/h", Kind: "error", Text: "failed", Observed: now},
	}})
	v.Selected = "/root/d"
	first := plainLines(v.renderRosterPane(42, 6, now))
	if !strings.Contains(strings.Join(first, "\n"), "d") {
		t.Fatalf("selection not in compact viewport: %q", first)
	}
	if firstText := strings.Join(first, "\n"); !strings.Contains(firstText, "↓ 4 more · 1 ◐ · 1 !") || strings.Contains(firstText, "↑") {
		t.Errorf("hidden directional status counts = %q", firstText)
	}
	start := v.RosterOffset
	v.renderRosterPane(42, 6, now)
	if v.RosterOffset != start {
		t.Fatalf("unstable viewport: %d -> %d", start, v.RosterOffset)
	}
	v.setDisplayFilter(true, v.Selected)
	v.Following, v.Unseen = false, 2
	v.Width, v.Height = 42, 6
	v.testHandleMouse('j', 3, 2)
	if !v.displayingChild() || v.Selected != "/root/d" || v.Following || v.Unseen != 2 {
		t.Fatalf("wheel changed feed/filter state: only=%v selected=%q following=%v unseen=%d", v.displayingChild(), v.Selected, v.Following, v.Unseen)
	}
	second := strings.Join(plainLines(v.renderRosterPane(42, 6, now)), "\n")
	if v.RosterOffset != start+1 || !strings.Contains(second, fmt.Sprintf("↑ %d more", v.RosterOffset)) {
		t.Errorf("missing directional overflow: %q", second)
	}
	v.RosterOffset, v.RosterManual = 5, true
	bottom := strings.Join(plainLines(v.renderRosterPane(42, 6, now)), "\n")
	if !strings.Contains(bottom, "g  ✗ failed") || strings.Contains(bottom, "↓") {
		t.Errorf("bottom row visibility = %q", bottom)
	}
}

func TestRosterDesignPaneGeometryAndHits(t *testing.T) {
	v := liveActivityTestView("/root", "/root/a", "/root/b")
	for _, size := range [][2]int{{30, 3}, {42, 6}, {80, 14}, {130, 12}} {
		lines := v.renderRosterPane(size[0], size[1], time.Now())
		if len(lines) != size[1] {
			t.Fatalf("%v: %d lines", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0]-1 {
				t.Fatalf("%v: wide line %q", size, line)
			}
		}
		for _, hit := range v.Hits {
			if hit.Row < 2 || hit.Row > size[1] || hit.First < 1 || hit.Last > size[0]-1 || hit.First > hit.Last {
				t.Fatalf("%v: invalid hit %+v", size, hit)
			}
		}
	}
}

func TestRosterCompactPrioritizesVisibilityAndStableSelection(t *testing.T) {
	end, metrics := activityui.RosterRange(4, 0, 0, 4)
	if end != 4 || metrics {
		t.Fatalf("four compact agents should all fit: end=%d metrics=%v", end, metrics)
	}
	v := liveActivityTestView("/root/a", "/root/b", "/root/c", "/root/d", "/root/e", "/root/f", "/root/g", "/root/h")
	v.Selected = "/root/h"
	v.renderRosterPane(80, 6, time.Now())
	v.RosterOffset, v.RosterManual = 0, true
	v.renderRosterPane(80, 6, time.Now())
	for _, hit := range v.Hits {
		if hit.Agent == "/root/d" {
			v.testPointAgent('\r', hit.Row, hit.First)
			break
		}
	}
	v.renderRosterPane(80, 6, time.Now())
	if v.Selected != "/root/d" || v.RosterOffset != 0 {
		t.Fatalf("visible selection moved viewport: selected=%s offset=%d", v.Selected, v.RosterOffset)
	}
}

func TestRosterStripWheelDoesNotScrollFeed(t *testing.T) {
	v := liveActivityTestView("/root/a", "/root/b")
	v.Render(80, 6, time.Now())
	v.FeedLines, v.FeedRows = 50, 3
	v.setDisplayFilter(false, v.Selected)
	v.Following, v.Unseen = true, 4
	selected, offset := v.Selected, v.Offset
	v.testHandleMouse('k', 2, 4)
	if !v.Following || v.displayingChild() || v.Unseen != 4 || v.Offset != offset || v.Selected != selected {
		t.Fatal("strip wheel changed feed or selection")
	}
}

func TestRosterDesignScrolledTreeCards(t *testing.T) {
	now := time.Now()
	v := liveActivityTestView("/root", "/root/a", "/root/b", "/root/b/x", "/root/b/y", "/root/c", "/root/d", "/root/e")
	rows := v.testRoster()
	// With b's parent off-screen, b is flat and its children draw no guide for it.
	for i, want := range map[int]string{3: "├ x", 4: "└ y"} {
		if name, _ := activityui.RosterTree(rows, i, 2); name != want {
			t.Errorf("scrolled tree %d = %q, want %q", i, name, want)
		}
	}
	cards := plainLines(v.RenderCards(rows[:3], 40, 9, now))
	// Card activity and metrics rows share the name column and tree guide.
	if !strings.HasPrefix(cards[3], " · ├ a") || !strings.HasPrefix(cards[4], "   │ Read") || !strings.HasPrefix(cards[5], "   │ ") {
		t.Errorf("card alignment = %q", cards)
	}

}

func TestNativeActivitySkipsMainInNavigationAndCounts(t *testing.T) {
	v := newActivityAdapter()
	v.ChildrenOnly = true
	v.testAudience()
	v.Apply(sessionpresentation.ActivityEvent{Kind: "agents", Agents: []sessionpresentation.ActivityAgent{{Name: "/root"}, {Name: "/root/a"}, {Name: "/root/b"}}})
	if v.Selected != "/root/a" {
		t.Fatalf("default selection = %q, want the first child", v.Selected)
	}
	var picks []string
	for range 4 {
		v.testShowAgent(1)
		picks = append(picks, v.Selected)
	}
	if !slices.Equal(picks, []string{"/root/a", "/root/b", "/root/b", "/root/b"}) {
		t.Fatalf("roster navigation picks = %q, want children only", picks)
	}

	for range 3 {
		v.testShowAgent(-1)
	}
	if v.displayingChild() || v.Selected != "/root/a" {
		t.Fatalf("stepping back reached %q (only=%v), want the unfiltered feed", v.Selected, v.displayingChild())
	}
	v.Hits = []activityui.Hit{{Row: 1, First: 1, Last: 10, Agent: "/root"}}
	v.setDisplayFilter(true, v.Selected)
	v.testPointAgent('\r', 1, 1)
	if v.displayingChild() || v.Selected != "/root/a" {
		t.Fatalf("clicking Main changed the remembered child to %q (only=%v)", v.Selected, v.displayingChild())
	}
}
