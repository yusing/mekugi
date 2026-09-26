package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestRosterDesignMessageOwnerAndAssignment(t *testing.T) {
	p := liveActivityPainter{}
	for _, tc := range []struct{ owner, from, to, want string }{
		{"/root/a", "/root/a", "/root", "→ main"},
		{"/root", "/root/a", "/root", "← a"},
		{"/root/a", "/root/a", "/root/b", "→ b"},
		{"/root/b", "/root/a", "/root/b", "← a"},
		{"/root/a/x", "/root/a/x", "/root/a", "→ a"},
		{"/root/a", "/root/a/x", "/root/a", "← a/x"},
	} {
		blocks := parseLiveActivity(activityPaneEntry{Agent: tc.owner, Kind: "reply", Text: "[`" + tc.from + "` -> `" + tc.to + "`] Message received:\nMessage body"})
		if len(blocks) != 1 || blocks[0].kind != "message" {
			t.Fatalf("parsed message = %+v", blocks)
		}
		for _, got := range []string{ansi.Strip(strings.Join(p.block(blocks[0], 80), "\n")), ansi.Strip(p.summary(blocks))} {
			if !strings.Contains(got, tc.want) || !strings.Contains(got, "Message body") {
				t.Errorf("%s -> %s owned by %s: %q, want direction %q and body", tc.from, tc.to, tc.owner, got, tc.want)
			}
		}
	}
	start := parseLiveActivity(activityPaneEntry{Kind: "start", Text: "Started · model high\nSpawn assignment:\nInspect parser.\n\n- Preserve behavior."})
	if len(start) != 1 || start[0].body != "Inspect parser.\n\n- Preserve behavior." {
		t.Fatalf("assignment body = %+v", start)
	}
	now := time.Now()
	v := liveActivityTestView("/root", "/root/a")
	if empty, _ := v.current(v.agents[0], now); ansi.Strip(empty) != "Read root.go" {
		t.Errorf("main without inbound = %q", empty)
	}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
		{Seq: 3, Agent: "/root/a", Kind: "reply", Text: "[`/root/a` -> `/root`] Message received:\nOlder inbound", Observed: now},
		{Seq: 4, Agent: "/root/a", Kind: "reply", Text: "[`/root/a` -> `/root`] Message received:\nLatest inbound", Observed: now},
	}})
	summary, _ := v.current(v.agents[0], now)
	if !strings.Contains(ansi.Strip(summary), "Latest inbound") || strings.Contains(ansi.Strip(summary), "Older inbound") || !strings.Contains(ansi.Strip(summary), "← a") {
		t.Fatalf("main inbound summary = %q", summary)
	}
}

func TestRosterDesignSeparateHeaderTreeAndMetrics(t *testing.T) {
	now := time.Now()
	v := newLiveActivityView()
	v.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{
		{Name: "/root", Role: "root-role"},
		{Name: "/root/a", Role: "explorer", Turns: 1},
		{Name: "/root/b", Responding: true},
		{Name: "/root/a/x"}, {Name: "/root/a/y"},
	}, Entries: []activityPaneEntry{{Seq: 1, Agent: "/root/a/x", Kind: "error", Text: "failed", Observed: now}}})
	rows := v.roster()
	var names []string
	for _, row := range rows {
		names = append(names, row.agent.Name)
	}
	if got := strings.Join(names, ","); got != "/root,/root/a,/root/a/x,/root/a/y,/root/b" {
		t.Fatalf("tree order = %s", got)
	}
	for _, tc := range []struct {
		i    int
		want string
	}{{2, "├ x"}, {3, "└ y"}} {
		name, _ := rosterTree(rows, tc.i, 0)
		if !strings.Contains(name, tc.want) {
			t.Errorf("tree %d = %q", tc.i, name)
		}
	}
	name, _ := rosterTree(rows, 3, 3)
	if name != "a/y" {
		t.Errorf("offscreen parent name = %q", name)
	}
	v.status = ""
	roster := strings.Join(plainLines(v.renderRosterPane(90, 12, now)), "\n")
	if !strings.Contains(roster, "5 agents · 1 responding · 1 error") || strings.Contains(roster, "FOLLOW") || strings.Contains(roster, "ACTIVITY") {
		t.Errorf("roster header = %q", roster)
	}
	feed := strings.Join(plainLines(v.render(90, 20, now)), "\n")
	if !strings.Contains(feed, "FOLLOW") || !strings.Contains(feed, "o only") {
		t.Errorf("feed chrome = %q", feed)
	}
	v.feedOnly = true
	feedOnly := strings.Join(plainLines(v.render(90, 20, now)), "\n")
	if !strings.Contains(feedOnly, "ACTIVITY") || strings.Contains(feedOnly, "click agent") {
		t.Errorf("feed-only chrome = %q", feedOnly)
	}
	strip := ansi.Strip(v.renderStrip(rows, 90))
	if !strings.Contains(strip, "main") || strings.Contains(strip, "/root") {
		t.Errorf("strip = %q", strip)
	}
	v.hovered = "/root/a"
	if hovered := v.renderStrip(rows, 90); !strings.Contains(hovered, "\x1b[4ma\x1b[24m") {
		t.Errorf("strip hover styling = %q", hovered)
	}
	table := v.metricTable(rows[:2], now)
	if metric := table[1]; strings.Contains(metric, "explorer") || !strings.Contains(metric, liveActivityDim+"T+"+liveActivityUndim+"1") || strings.Contains(metric, "n/a") {
		t.Errorf("metric style = %q", metric)
	}
	if strings.Contains(table[0], "root-role") {
		t.Error("root role leaked into metrics")
	}
}

func TestRosterDesignCompactViewportAndWheel(t *testing.T) {
	now := time.Now()
	v := liveActivityTestView("/root/a", "/root/b", "/root/c", "/root/d", "/root/e", "/root/f", "/root/g", "/root/h")
	v.agents[0].Responding = true
	v.agents[7].Responding = true
	// A responding agent's earlier error counts once, as its visible ◐ status.
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
		{Seq: 9, Agent: "/root/g", Kind: "error", Text: "failed", Observed: now},
		{Seq: 10, Agent: "/root/h", Kind: "error", Text: "failed", Observed: now},
	}})
	v.selected = "/root/d"
	first := plainLines(v.renderRosterPane(42, 6, now))
	if !strings.Contains(strings.Join(first, "\n"), "d") {
		t.Fatalf("selection not in compact viewport: %q", first)
	}
	if firstText := strings.Join(first, "\n"); !strings.Contains(firstText, "↓ 4 more · 1 ◐ · 1 !") || strings.Contains(firstText, "↑") {
		t.Errorf("hidden directional status counts = %q", firstText)
	}
	start := v.rosterOffset
	v.renderRosterPane(42, 6, now)
	if v.rosterOffset != start {
		t.Fatalf("unstable viewport: %d -> %d", start, v.rosterOffset)
	}
	v.only, v.following, v.unseen = true, false, 2
	v.width, v.height = 42, 6
	v.handleMouse('j', 3, 2)
	if !v.only || v.selected != "/root/d" || v.following || v.unseen != 2 {
		t.Fatalf("wheel changed feed/filter state: only=%v selected=%q following=%v unseen=%d", v.only, v.selected, v.following, v.unseen)
	}
	second := strings.Join(plainLines(v.renderRosterPane(42, 6, now)), "\n")
	if v.rosterOffset != start+1 || !strings.Contains(second, fmt.Sprintf("↑ %d more", v.rosterOffset)) {
		t.Errorf("missing directional overflow: %q", second)
	}
	v.rosterOffset, v.rosterManual = 5, true
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
		for _, hit := range v.hits {
			if hit.row < 2 || hit.row > size[1] || hit.first < 1 || hit.last > size[0]-1 || hit.first > hit.last {
				t.Fatalf("%v: invalid hit %+v", size, hit)
			}
		}
	}
}

func TestRosterCompactPrioritizesVisibilityAndStableSelection(t *testing.T) {
	end, metrics := rosterRange(4, 0, 0, 4)
	if end != 4 || metrics {
		t.Fatalf("four compact agents should all fit: end=%d metrics=%v", end, metrics)
	}
	v := liveActivityTestView("/root/a", "/root/b", "/root/c", "/root/d", "/root/e", "/root/f", "/root/g", "/root/h")
	v.selected = "/root/h"
	v.renderRosterPane(80, 6, time.Now())
	v.rosterOffset, v.rosterManual = 0, true
	v.renderRosterPane(80, 6, time.Now())
	for _, hit := range v.hits {
		if hit.agent == "/root/d" {
			v.pointAgent('\r', hit.row, hit.first)
			break
		}
	}
	v.renderRosterPane(80, 6, time.Now())
	if v.selected != "/root/d" || v.rosterOffset != 0 {
		t.Fatalf("visible selection moved viewport: selected=%s offset=%d", v.selected, v.rosterOffset)
	}
}

func TestRosterStripWheelDoesNotScrollFeed(t *testing.T) {
	v := liveActivityTestView("/root/a", "/root/b")
	v.render(80, 6, time.Now())
	v.feedLines, v.feedRows = 50, 3
	v.following, v.only, v.unseen = true, false, 4
	selected, offset := v.selected, v.offset
	v.handleMouse('k', 2, 4)
	if !v.following || v.only || v.unseen != 4 || v.offset != offset || v.selected != selected {
		t.Fatal("strip wheel changed feed or selection")
	}
}

func TestRosterDesignScrolledTreeCardsAndSelection(t *testing.T) {
	now := time.Now()
	v := liveActivityTestView("/root", "/root/a", "/root/b", "/root/b/x", "/root/b/y", "/root/c", "/root/d", "/root/e")
	rows := v.roster()
	// With b's parent off-screen, b is flat and its children draw no guide for it.
	for i, want := range map[int]string{3: "├ x", 4: "└ y"} {
		if name, _ := rosterTree(rows, i, 2); name != want {
			t.Errorf("scrolled tree %d = %q, want %q", i, name, want)
		}
	}
	summaryColumn := func(lines []string, agent string) int {
		for _, line := range plainLines(lines) {
			if i := strings.Index(line, "Read "+agent+".go"); i >= 0 && strings.Contains(line, " "+agent+" ") {
				return ansi.StringWidth(line[:i])
			}
		}
		return -1
	}
	v.selected = "/root"
	top := summaryColumn(v.renderRosterPane(60, 5, now), "a")
	v.selected = "/root/e"
	bottom := summaryColumn(v.renderRosterPane(60, 5, now), "e")
	if top < 0 || top != bottom {
		t.Errorf("summary column moved while scrolling: %d -> %d", top, bottom)
	}
	cards := plainLines(v.renderCards(rows[:3], 40, 9, now))
	// Card activity and metrics rows share the name column and tree guide.
	if !strings.HasPrefix(cards[3], " · ├ a") || !strings.HasPrefix(cards[4], "   │ Read") || !strings.HasPrefix(cards[5], "   │ ") {
		t.Errorf("card alignment = %q", cards)
	}
	v.only = false
	v.selectAgent(1)
	if v.only {
		t.Error("selecting an agent enabled the feed filter")
	}
}
