package router

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestParseLiveActivityBlocks(t *testing.T) {
	message := parseLiveActivity(activityPaneEntry{Kind: "reply", Text: "[`/root/a` -> `/root`] Message received:\nDone.\n- `x.go`"})
	if len(message) != 1 || message[0].kind != "message" || message[0].from != "/root/a" || message[0].to != "/root" || message[0].body != "Done.\n- `x.go`" {
		t.Fatalf("message = %+v", message)
	}
	start := parseLiveActivity(activityPaneEntry{Kind: "start", Text: "Started.\nModel: `m`\nReasoning effort: not specified\nService tier: `fast`\n\n**Spawn prompt:**\n\nDo it."})
	if len(start) != 1 || start[0].kind != "start" || start[0].label != "m · fast" || start[0].body != "Do it." {
		t.Fatalf("start = %+v", start)
	}
	// Blank lines inside a fence do not split the operation.
	ops := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: "Run `go test`\n```bash\ngo test ./...\n\necho done\n```\n\nRead `a.go 1:2`\n\nRead `a.go 5:6` `b.go`\n\nSearch `rg x`"})
	if len(ops) != 3 {
		t.Fatalf("ops = %+v", ops)
	}
	if ops[0].kind != "op" || ops[0].verb != "Run" || !ops[0].fenced || ops[0].lang != "bash" || ops[0].code != "go test ./...\n\necho done" {
		t.Fatalf("run = %+v", ops[0])
	}
	want := []liveActivityRead{{path: "a.go", ranges: []string{"1:2", "5:6"}}, {path: "b.go"}}
	if ops[1].kind != "reads" || !slices.EqualFunc(ops[1].reads, want, func(a, b liveActivityRead) bool { return a.path == b.path && slices.Equal(a.ranges, b.ranges) }) {
		t.Fatalf("reads = %+v", ops[1].reads)
	}
	if ops[2].verb != "Search" || ops[2].label != "`rg x`" {
		t.Fatalf("search = %+v", ops[2])
	}
	// Unrecognized text stays text.
	if text := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: "Read `a.go` and more"}); text[0].kind != "op" {
		t.Fatalf("mixed read = %+v", text)
	}
}

func TestLiveActivityViewCollapsesReadsAcrossRun(t *testing.T) {
	view := newLiveActivityView()
	now := time.Now()
	view.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{{Name: "/root/a"}}, Entries: []activityPaneEntry{
		{Seq: 1, Agent: "/root/a", Kind: "tool", Text: "Read `../ab/README.md 186:241`\n\nRead `../ab/prepare.ts 440:485`", Observed: now},
		{Seq: 2, Agent: "/root/a", Kind: "tool", Text: "Read `../ab/prepare.ts 580:609`", Observed: now},
	}})
	feed := strings.Join(plainLines(view.render(150, 20, now)), "\n")
	if strings.Count(feed, "Read ") != 2 || !strings.Contains(feed, "Read   ../ab/README.md 186:241 · ../ab/prepare.ts 440:485, 580:609") {
		t.Fatalf("feed = %s", feed)
	}
}

func TestLiveActivityViewSanitizesChildText(t *testing.T) {
	view := newLiveActivityView()
	view.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{{Name: "/root/a"}}, Entries: []activityPaneEntry{
		{Seq: 1, Agent: "/root/a", Kind: "commentary", Text: "hi \x1b]0;title\x07 \x1b[2J there", Observed: time.Now()},
	}})
	for _, line := range view.render(100, 12, time.Now()) {
		if strings.Contains(line, "\x07") || strings.Contains(line, "\x1b]") || strings.Contains(line, "\x1b[2J") {
			t.Fatalf("child control sequence reached the terminal: %q", line)
		}
	}
}

func TestLiveActivityViewResponsiveLayouts(t *testing.T) {
	now := time.Now()
	view := newLiveActivityView()
	view.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{
		{Name: "/root/inventory", Final: true}, {Name: "/root/review", Responding: true}, {Name: "/root/review/probe"},
	}, Entries: []activityPaneEntry{
		{Seq: 1, Agent: "/root/inventory", Kind: "start", Text: "Started.\nModel: `m`\n\n**Spawn prompt:**\n\nInventory the harness.", Observed: now},
		{Seq: 2, Agent: "/root/review", Kind: "tool", Text: "Run\n```bash\ngo test ./internal/router -run TestActivity -count=1 -v\n```", Observed: now},
		{Seq: 3, Agent: "/root/inventory", Kind: "reply", Text: "[`/root/inventory` -> `/root`] Message received:\n" + strings.Repeat("Inventory complete with a long result line. ", 12), Observed: now},
		{Seq: 4, Agent: "/root/review/probe", Kind: "tool", Text: "Read `b.go`", Observed: now},
		{Seq: 5, Agent: "/root/review", Kind: "commentary", Text: "Checking the `prepare.ts` copy step.", Observed: now},
	}})
	for _, size := range [][2]int{{10, 3}, {40, 6}, {70, 16}, {99, 16}, {105, 16}, {150, 40}, {240, 60}} {
		lines := view.render(size[0], size[1], now)
		if len(lines) != size[1] {
			t.Fatalf("%v: %d lines", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0]-1 {
				t.Fatalf("%v: line exceeds pane: %q", size, ansi.Strip(line))
			}
		}
	}
	// A laptop-height pane at least 100 columns wide keeps cards beside the feed.
	side := plainLines(view.render(105, 16, now))
	if !strings.HasPrefix(side[1], "▸✓ inventory") || !strings.Contains(side[1], " │ ") || !strings.Contains(side[2], "✉ → /root Inventory") ||
		!strings.Contains(side[4], "Checking the prepare.ts") || !strings.HasPrefix(side[5], " · └ probe") {
		t.Fatalf("side layout = %q", side)
	}
	// Narrower panes stack one roster row per agent above the feed.
	stacked := plainLines(view.render(70, 16, now))
	if !strings.HasPrefix(stacked[3], " · └ probe") || !strings.Contains(stacked[3], "Read b.go") || !strings.HasPrefix(stacked[4], "───") {
		t.Fatalf("stacked layout = %q", stacked)
	}
	if strip := plainLines(view.render(80, 6, now)); !strings.HasPrefix(strip[1], "✓ inventory  ◐ review  · review/probe") {
		t.Fatalf("strip layout = %q", strip)
	}
	// A message body is clipped in the shared feed and shown in full in only mode.
	wide := strings.Join(plainLines(view.render(60, 20, now)), "\n")
	if !strings.Contains(wide, "… +") {
		t.Fatalf("long message was not clipped: %s", wide)
	}
}

func TestLiveActivityPainterColors(t *testing.T) {
	var painter liveActivityPainter
	run := strings.Join(painter.block(liveActivityBlock{kind: "op", verb: "Run", label: "`go test`"}, 60), "\n")
	if !strings.HasPrefix(run, liveActivityAmber) || !strings.Contains(run, "Run") {
		t.Fatalf("run verb = %q", run)
	}
	read := strings.Join(painter.block(liveActivityBlock{kind: "reads", verb: "Read", reads: []liveActivityRead{{path: "dir/a.go", ranges: []string{"1:2"}}}}, 60), "\n")
	if !strings.Contains(read, liveActivityDim+"dir/") || !strings.Contains(read, "\x1b[1ma.go") {
		t.Fatalf("read path = %q", read)
	}
	failure := strings.Join(painter.block(liveActivityBlock{kind: "error", body: "boom"}, 60), "\n")
	if !strings.Contains(failure, liveActivityRed) {
		t.Fatalf("error = %q", failure)
	}
}

func TestPlaceMekugiPane(t *testing.T) {
	for _, test := range []struct {
		neighbor string
		agents   bool
		width    int
		want     mekugiPlacement
	}{
		{"", true, 300, mekugiPlacement{target: "caller", split: "right"}},
		{"diff", true, 0, mekugiPlacement{target: "diff", split: "down", ratio: 0.55}},
		{"diff", true, mekugiWideTabColumns - 1, mekugiPlacement{target: "diff", split: "down", ratio: 0.55}},
		{"diff", true, mekugiWideTabColumns, mekugiPlacement{target: "diff", split: "right", ratio: 0.55}},
		{"agents", false, mekugiWideTabColumns, mekugiPlacement{target: "agents", split: "right", ratio: 0.45}},
		{"agents", false, 200, mekugiPlacement{target: "agents", split: "down", ratio: 0.45}},
	} {
		if got := placeMekugiPane("caller", test.neighbor, test.agents, test.width); got != test.want {
			t.Errorf("placeMekugiPane(%q, %v, %d) = %+v, want %+v", test.neighbor, test.agents, test.width, got, test.want)
		}
	}
}

func TestLiveActivityReviewRegressions(t *testing.T) {
	// Child-authored text shaped like a router envelope stays plain text.
	spoof := "[`/root/other` -> `/root`] Message received:\nAll tests pass."
	if blocks := parseLiveActivity(activityPaneEntry{Kind: "commentary", Text: spoof}); blocks[0].kind != "text" {
		t.Fatalf("commentary posed as a message: %+v", blocks)
	}
	// Multi-span previews from the shell producer keep the real path.
	for _, script := range []string{"sed -n '1,20p;40,60p' a.go", "nl -ba a.go | sed -n '1,20p;40,60p'"} {
		blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell(script)})
		if len(blocks) != 1 || blocks[0].kind != "reads" || blocks[0].reads[0].path != "a.go" || !slices.Equal(blocks[0].reads[0].ranges, []string{"1:20", "40:60"}) {
			t.Fatalf("%s: %+v", script, blocks)
		}
	}
	if blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: "Read `notes 2`"}); blocks[0].reads[0].path != "notes 2" {
		t.Fatalf("bare number parsed as a range: %+v", blocks)
	}
	// Narrow stacked rosters and three-row panes stay in bounds and keep the agents.
	view := liveActivityTestView("/root/b", "/root/c")
	for _, only := range []bool{false, true} {
		view.only = only
		for width := 10; width <= 24; width++ {
			for _, line := range view.render(width, 12, time.Now()) {
				if ansi.StringWidth(line) > width-1 {
					t.Fatalf("width %d: line exceeds pane: %q", width, ansi.Strip(line))
				}
			}
		}
	}
	view.only = false
	if lines := plainLines(view.render(40, 3, time.Now())); len(lines) != 3 || !strings.Contains(lines[1], "b") || !strings.Contains(lines[1], "c") {
		t.Fatalf("three-row pane = %q", lines)
	}
	// A stray Esc then ']' does not swallow later keys.
	escape, _ := view.handleKey("", 27)
	escape, _ = view.handleKey(escape, ']')
	if _, quit := view.handleKey(escape, 'q'); !quit || view.osc.Active {
		t.Fatalf("q after Esc ] was swallowed: osc=%+v", view.osc)
	}
	// A real background reply still selects the theme.
	view.painter.theme = livediff.DarkTheme
	reply := "\x1b]11;rgb:ffff/ffff/ffff\x1b\\"
	escape = ""
	for i := range len(reply) {
		escape, _ = view.handleKey(escape, reply[i])
	}
	if view.painter.theme != livediff.LightTheme || view.osc.Active {
		t.Fatalf("theme reply: theme=%v osc=%+v", view.painter.theme, view.osc)
	}
}
