package router

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/charmbracelet/x/ansi"
)

func liveActivityTestView(names ...string) *activityAdapter {
	view := newActivityAdapter()
	var agents []sessionpresentation.ActivityAgent
	var entries []sessionpresentation.ActivityEntry
	now := time.Now()
	for i, name := range names {
		agents = append(agents, sessionpresentation.ActivityAgent{Name: name})
		entries = append(entries, sessionpresentation.ActivityEntry{Seq: uint64(i + 1), Agent: name, Kind: "tool", Text: "Read `" + name + ".go`", Observed: now})
	}
	view.Apply(sessionpresentation.ActivityEvent{Kind: "snapshot", Agents: agents, Entries: entries})
	return view
}

func plainLines(lines []string) []string {
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = ansi.Strip(line)
	}
	return plain
}

func TestLiveActivityViewRosterTreeOverflowAndSelection(t *testing.T) {
	view := liveActivityTestView("/root/a", "/root/b", "/root/a/x", "/root/c", "/root/a/y", "/root/d", "/root/e")
	view.Agents[0].Responding = true
	view.Agents[1].Final = true
	view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 8, Agent: "/root/c", Kind: "error", Text: "failed", Observed: time.Now()}}})
	var order []string
	for _, row := range view.testRoster() {
		order = append(order, strings.Repeat(">", row.Depth)+row.Agent.Name)
	}
	if got := strings.Join(order, " "); got != "/root/a >/root/a/x >/root/a/y /root/b /root/c /root/d /root/e" {
		t.Fatalf("roster order = %s", got)
	}
	lines := plainLines(view.Render(60, 20, time.Now()))
	if !strings.HasPrefix(lines[0], "AGENTS  7 agents · 1 responding · 1 error") || !strings.HasSuffix(lines[0], "FOLLOW") {
		t.Fatalf("header = %q", lines[0])
	}
	// Six rows compact to five agents and directional overflow.
	if !strings.HasPrefix(lines[1], " ◐ a") || !strings.Contains(lines[2], "├ x") ||
		lines[6] != "↓ 2 more" {
		t.Fatalf("roster = %q", lines[1:7])
	}
	view.Selected = "/root/e"
	styled := view.Render(60, 20, time.Now())
	lines = plainLines(styled)
	// Selection fills its row in place, without a marker column.
	fill := view.Painter.Theme.SelectionBackground()
	if view.Selected != "/root/e" || !slices.ContainsFunc(styled[1:7], func(line string) bool {
		return strings.HasPrefix(line, fill) && strings.Contains(ansi.Strip(line), " · e")
	}) ||
		!slices.ContainsFunc(lines[1:7], func(line string) bool { return strings.HasPrefix(line, " · e") }) {
		t.Fatalf("selection scroll: selected=%s roster=%q", view.Selected, lines[1:7])
	}
	for _, line := range view.Render(60, 20, time.Now()) {
		if ansi.StringWidth(line) > 59 {
			t.Fatalf("line exceeds pane: %q", ansi.Strip(line))
		}
	}
}

func TestLiveActivityViewClampOnlyModeAndPausedCount(t *testing.T) {
	view := liveActivityTestView("/root/a", "/root/b")
	long := "Plan\n```go\n" + strings.Repeat("line\n", 20) + "```"
	view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 3, Agent: "/root/a", Text: long, Observed: time.Now()}}})
	all := strings.Join(plainLines(view.Render(80, 40, time.Now())), "\n")
	if !strings.Contains(all, "… +") || !strings.Contains(all, "  line") {
		t.Fatalf("clamped feed = %s", all)
	}
	view.setDisplayFilter(true, view.Selected)
	view.Follow()
	only := plainLines(view.Render(80, 40, time.Now()))
	joined := strings.Join(only, "\n")
	if !strings.Contains(joined, "… +") || strings.Count(joined, "  line") >= 20 || strings.Contains(joined, "● b") {
		t.Fatalf("only feed = %s", joined)
	}
	if !strings.HasPrefix(only[len(only)-1], "ONLY ·") || !strings.Contains(only[0], "only a (1/2)") {
		t.Fatalf("only chrome = %q / %q", only[0], only[len(only)-1])
	}
	view.Render(80, 5, time.Now()) // Make scrollback possible with five-row excerpts.
	view.ScrollKey('k')
	view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{
		{Seq: 4, Agent: "/root/a", Text: "more", Observed: time.Now()},
		{Seq: 5, Agent: "/root/b", Text: "hidden", Observed: time.Now()},
		{Seq: 4, Agent: "/root/a", Text: "duplicate", Observed: time.Now()},
	}})
	if header := plainLines(view.Render(80, 5, time.Now()))[0]; !strings.HasSuffix(header, "PAUSED · 1 new") {
		t.Fatalf("paused header = %q", header)
	}
	view.ScrollKey('G')
	if header := plainLines(view.Render(80, 5, time.Now()))[0]; !strings.HasSuffix(header, "FOLLOW") {
		t.Fatalf("follow header = %q", header)
	}
}

func TestLiveActivitySnippetClick(t *testing.T) {
	long := "Plan\n```go\n" + strings.Repeat("line\n", 20) + "```"
	hint := regexp.MustCompile(`… \+\d+ lines$`)
	for _, size := range [][2]int{{80, 40}, {140, 40}} {
		view := liveActivityTestView("/root/a", "/root/b")
		now := time.Now()
		view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 3, Agent: "/root/a", Text: long, Observed: now}}})
		lines := view.Render(size[0], size[1], now)
		clipped := func(lines []string) bool {
			return slices.ContainsFunc(plainLines(lines), func(line string) bool { return hint.MatchString(strings.TrimRight(line, " ")) })
		}
		row := slices.IndexFunc(plainLines(lines), func(line string) bool { return hint.MatchString(strings.TrimRight(line, " ")) }) + 1
		if row == 0 {
			t.Fatalf("%v: no clipped snippet: %q", size, plainLines(lines))
		}
		column := view.FeedLeft + 4
		if !view.testHandleMouse('h', row-2, column) || !strings.Contains(view.Render(size[0], size[1], now)[row-1], "\x1b[4m… +") {
			t.Fatalf("%v: hovering a collapsed snippet did not underline its hint", size)
		}
		if !view.testHandleMouse('h', 1, column) || view.Render(size[0], size[1], now)[row-1] != lines[row-1] {
			t.Fatalf("%v: leaving the snippet kept its underline", size)
		}
		// Clicks outside a snippet leave it clipped.
		view.testHandleMouse('\r', 1, column)
		if !clipped(view.Render(size[0], size[1], now)) {
			t.Fatalf("%v: header click expanded the snippet", size)
		}
		if !view.testHandleMouse('\r', row, column) {
			t.Fatalf("%v: click did not redraw", size)
		}
		if view.Opening == (activityui.Snippet{}) || !clipped(view.Render(size[0], size[1], now)) {
			t.Fatalf("%v: click did not request dialog while retaining clipped feed", size)
		}
	}
}

func TestLiveActivityViewTinyAndNarrowPanes(t *testing.T) {
	view := liveActivityTestView("/root/explorer/deeply/nested/worker", "/root/b", "/root/c")
	for _, size := range [][2]int{{10, 3}, {24, 6}, {30, 9}} {
		lines := view.Render(size[0], size[1], time.Now())
		if len(lines) != size[1] {
			t.Fatalf("%v: %d lines", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0]-1 {
				t.Fatalf("%v: line exceeds pane: %q", size, ansi.Strip(line))
			}
		}
	}
	if lines := plainLines(view.Render(40, 6, time.Now())); !strings.HasPrefix(lines[1], "· explorer/deeply/nested/worker  · b") {
		t.Fatalf("short pane roster = %q", lines)
	}
	if got := activityui.Middle("/root/explorer/deeply/nested/worker", 20); !strings.HasSuffix(got, "…worker") || ansi.StringWidth(got) != 20 {
		t.Fatalf("middle = %q", got)
	}
}

func TestLiveActivityRosterShowsUsageByLayout(t *testing.T) {
	view := liveActivityTestView("/root/a", "/root/b")
	view.Agents[0].InputTokens, view.Agents[0].OutputTokens = 146_800, 3_200
	for _, width := range []int{140} {
		lines := plainLines(view.Render(width, 20, time.Now()))
		row := slices.IndexFunc(lines, func(line string) bool { return strings.Contains(line, "↑") })
		// The roster status ends the row; cards end it at the column divider.
		status, _, _ := strings.Cut(lines[max(0, row)], " │ ")
		want := "↑ 146.8K ↓ 3.2K"
		if width >= 100 {
			want = "↑ 146.8K"
		}
		if row < 0 || !strings.HasSuffix(strings.TrimRight(status, " "), want) {
			t.Fatalf("width %d: usage missing: %q", width, lines)
		}
		if slices.ContainsFunc(lines, func(line string) bool { return strings.Count(line, "↑") > 1 }) {
			t.Fatalf("width %d: agent without usage shows tokens: %q", width, lines)
		}
	}
}

func TestLiveActivityCombinedRosterShowsTimerCostAndUsage(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	view := liveActivityTestView("/root/a", "/root/b")
	view.Agents[0].Started = now.Add(-8 * time.Minute)
	view.Agents[0].WorkTimer = activitypresentation.WorkTimer{Known: true, ElapsedNS: int64(7*time.Minute + 57*time.Second)}
	view.Agents[0].LastResponse = now.Add(-3 * time.Second)
	view.Agents[0].InputTokens, view.Agents[0].OutputTokens = 1_000, 0
	view.Agents[0].Turns, view.Agents[0].Roundtrips, view.Agents[0].Cost, view.Agents[0].CostKnown = 2, 2, 1.2, true
	view.Agents[1].Started = now.Add(-7 * time.Minute)
	view.Agents[1].WorkTimer = activitypresentation.WorkTimer{Known: true, ElapsedNS: int64(5 * time.Minute)}
	view.Agents[1].LastResponse = now.Add(-2 * time.Minute)
	view.Agents[1].InputTokens, view.Agents[1].OutputTokens = 500, 0
	view.Agents[1].Turns, view.Agents[1].Roundtrips, view.Agents[1].Cost, view.Agents[1].CostKnown = 1, 1, 0, false
	lines := plainLines(view.Render(90, 20, now))
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	first := slices.IndexFunc(lines, func(line string) bool { return strings.Contains(line, "$1.20") })
	second := slices.IndexFunc(lines, func(line string) bool { return strings.Contains(line, "5m · ") })
	// Idle agents retain accumulated active work. Unknown cost is
	// omitted rather than shown as n/a or zero.
	if first < 0 || second < 0 || first != second-1 || !strings.Contains(lines[first], "7m57s · 3s ago ↑ 1K ↓ 0") ||
		!strings.Contains(lines[first], "$1.20 T+2") || !strings.Contains(lines[second], "5m · 2m ago") ||
		!strings.Contains(lines[second], "↑ 500 ↓ 0 T+1") || strings.Contains(lines[second], "$") || strings.Contains(lines[second], "n/a") {
		t.Fatalf("combined roster metrics = %q", lines)
	}
}

func TestLiveActivityRosterUsesAvailableWidth(t *testing.T) {
	view := liveActivityTestView("/root/review_stock_preview")
	lines := plainLines(view.Render(80, 20, time.Now()))
	if !strings.Contains(lines[1], "review_stock_preview  ") || strings.Contains(lines[1], "…") {
		t.Fatalf("roster name clipped or padded unexpectedly: %q", lines[1])
	}
	view = liveActivityTestView("/root/very/long/agent/name/that/could/eat/the/whole/roster", "/root/b")
	lines = plainLines(view.Render(60, 20, time.Now()))
	if !strings.Contains(lines[2], "Read b.go") {
		t.Fatalf("long name obscured short agent's summary: %q", lines[2])
	}
}

func TestLiveActivityViewSummarizesCodeModeBatches(t *testing.T) {
	view := liveActivityTestView("/root/a", "/root/b")
	batch := "Read `a.go`\n\nRun `go test`\n```bash\ngo test ./...\n\necho done\n```\n\nRead `c.go`\n\nRun JavaScript · other code"
	view.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 3, Agent: "/root/a", Kind: "tool", Text: batch, Observed: time.Now()}}})
	lines := plainLines(view.renderRosterPane(120, 6, time.Now()))
	// The roster shows the latest operation of the batch.
	if !strings.Contains(lines[1], "Run JavaScript · other code · +3 more") {
		t.Fatalf("batch roster row = %q", lines[1])
	}
	if strings.Contains(lines[2], "more") {
		t.Fatalf("single operation marked as a batch: %q", lines[2])
	}
}
