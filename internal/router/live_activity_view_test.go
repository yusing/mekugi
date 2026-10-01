package router

import (
	"slices"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
)

func TestEditHoverDecoratesOnlyPointedText(t *testing.T) {
	for _, main := range []bool{false, true} {
		v := newLiveActivityView()
		v.conversation = main
		v.feedTop, v.feedLeft, v.feedRight = 1, 1, 100
		target := liveActivitySnippet{run: 1, block: editNavigationSnippet}
		bar := activityui.Green + "━━━━━━━━" + activityui.Red + "\x1b[39m" + activityui.Dim + activityui.Undim
		feed := liveActivityFeed{
			lines: []string{
				"│ " + activityui.Green + "Edited" + activityui.Reset + "  src/a ━━━━━━━━.go   +15 -1  " + bar,
				"└         ─file.go   +30     " + bar,
			},
			heads: []int{0, 0}, snippets: []liveActivitySnippet{target, target}, questions: []uint64{0, 0},
		}
		v.viewport(feed, 2)
		for _, hover := range []int{0, 1, -1} {
			if !v.pointSnippet('h', hover+1, 10) {
				t.Fatalf("moving hover to row %d did not request redraw", hover)
			}
			rows := v.viewport(feed, 2)
			for y, row := range rows {
				want := vt.NewEmulator(100, 1)
				got := vt.NewEmulator(100, 1)
				_, _ = want.Write([]byte(feed.lines[y] + "!"))
				_, _ = got.Write([]byte(row + "!"))
				for x := range ansi.StringWidth(row) + 1 {
					expected := *want.CellAt(x, 0)
					if y == hover && x < ansi.StringWidth(row)-8 && !strings.ContainsAny(expected.Content, " │└") {
						expected.Style.Underline = uv.UnderlineSingle
					}
					cell := got.CellAt(x, 0)
					if cell.Content != expected.Content || !cell.Style.Equal(&expected.Style) {
						t.Errorf("main=%t hover=%d cell (%d,%d) = %#v, want %#v", main, hover, x, y, cell, expected)
					}
				}
				want.Close()
				got.Close()
			}
		}
		// A stationary pointer stays on its screen row when content scrolls.
		v.pointSnippet('h', 2, 10)
		feed.lines = append(feed.lines, "unrelated output")
		feed.heads = append(feed.heads, 2)
		feed.snippets = append(feed.snippets, liveActivitySnippet{})
		feed.questions = append(feed.questions, 0)
		for _, following := range []bool{false, true} {
			v.following, v.offset = following, 1
			for _, row := range v.viewport(feed, 2) {
				if strings.Contains(row, "\x1b[4m") {
					t.Fatal("hover followed the edit away from the pointer")
				}
			}
		}
	}
}

func TestLiveActivityScrollStopsAtLastFullViewport(t *testing.T) {
	v := newLiveActivityView()
	v.conversation = true
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: strings.Repeat("Content row\n", 30) + "Last row"}}})
	feed := v.renderFeed(80, 8)
	bottom := v.viewport(feed, 8)
	for _, key := range []byte{'j', terminalui.PaneWheelDown, ' ', 'G'} {
		v.scrollKey(key)
		if got := v.viewport(feed, 8); !slices.Equal(got, bottom) {
			t.Fatalf("key %d scrolled past the bottom: %q", key, got)
		}
	}
	v.offset = len(feed.lines) - 1 // A question jump near the end must also clamp.
	if got := v.viewport(feed, 8); !slices.Equal(got, bottom) {
		t.Fatal("near-end jump left blank space below the last row")
	}
	if got := v.viewport(feed, 16); got[15] != feed.lines[len(feed.lines)-1] {
		t.Fatal("resizing left blank space below the last row")
	}
	v.viewport(feed, len(feed.lines)+5)
	if v.offset != 0 {
		t.Fatal("content shorter than the viewport remained scrolled")
	}
}

func TestLiveActivityJavaScriptStableIndent(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		painter := activityui.Painter{Theme: theme}
		for _, width := range []int{24, 80} {
			for _, source := range []string{
				"text(1);",
				"text(1);\ntext(2);",
				`const tool = ALL_TOOLS.find(x => /search_openai_docs$/.test(x.name)); text(tool);`,
			} {
				blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: "Run JavaScript\n" + toolActivityFenced("javascript", source)})
				if len(blocks) != 1 {
					t.Fatalf("blocks = %+v", blocks)
				}
				rows := plainLines(painter.Block(blocks[0], width))
				if len(rows) < 2 || strings.TrimSpace(rows[0]) != "Run JavaScript" {
					t.Fatalf("heading shares source: %q", rows)
				}
				for _, row := range rows[1:] {
					if !strings.HasPrefix(row, "  │ ") || ansi.StringWidth(row) > width {
						t.Fatalf("width %d: inconsistent code gutter or overflow: %q", width, row)
					}
				}
			}
		}
	}
}

func TestParseLiveActivityBlocks(t *testing.T) {
	message := parseLiveActivity(activityPaneEntry{Kind: "reply", message: &activityMessage{from: "/root/a", to: "/root", text: "Done.\n- `x.go`"}})
	if len(message) != 1 || message[0].Kind != "message" || message[0].From != "/root/a" || message[0].To != "/root" || message[0].Body != "Done.\n- `x.go`" {
		t.Fatalf("message = %+v", message)
	}
	start := parseLiveActivity(activityPaneEntry{Kind: "start", start: &activityStart{model: "m", effort: "high", tier: "fast"}})
	if len(start) != 1 || start[0].Kind != "start" || start[0].Label != "`m` `high` `fast`" || start[0].Body != "" {
		t.Fatalf("start = %+v", start)
	}
	// Blank lines inside a fence do not split the operation.
	ops := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: "Run `go test`\n```bash\ngo test ./...\n\necho done\n```\n\nRead `a.go 1:2`\n\nRead `a.go 5:6` `b.go`\n\nSearch `rg x`"})
	if len(ops) != 3 {
		t.Fatalf("ops = %+v", ops)
	}
	if ops[0].Kind != "op" || ops[0].Verb != "Run" || !ops[0].Fenced || ops[0].Lang != "bash" || ops[0].Code != "go test ./...\n\necho done" {
		t.Fatalf("run = %+v", ops[0])
	}
	want := []activityui.Read{{Path: "a.go", Ranges: []string{"1:2", "5:6"}}, {Path: "b.go"}}
	if ops[1].Kind != "reads" || !slices.EqualFunc(ops[1].Reads, want, func(a, b activityui.Read) bool { return a.Path == b.Path && slices.Equal(a.Ranges, b.Ranges) }) {
		t.Fatalf("reads = %+v", ops[1].Reads)
	}
	if ops[2].Verb != "Search" || len(ops[2].Reads) != 1 || ops[2].Reads[0].Path != "rg x" {
		t.Fatalf("search = %+v", ops[2])
	}
	// Unrecognized text stays text.
	if text := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: "Read `a.go` and more"}); text[0].Kind != "op" {
		t.Fatalf("mixed read = %+v", text)
	}
}

func TestLiveActivityRendersSpawnAssignment(t *testing.T) {
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-effective", "input": []any{journalTestAssignment("/root/explorer", "NEW_TASK", "Inspect parser.\n\n- Preserve behavior.")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	view := newLiveActivityView()
	view.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{{Name: "/root/explorer"}}, Entries: []activityPaneEntry{{
		Seq: 1, Agent: "/root/explorer", Kind: "start", start: nativeSubagentStart(&request), Observed: now,
		assignment: &activityAssignment{id: "task-1", from: "/root", to: "/root/explorer", text: "Inspect parser.\n\n- Preserve behavior."},
	}}})
	view.only, view.selected = true, "/root/explorer"
	frame := strings.Join(plainLines(view.render(100, 20, now)), "\n")
	for _, want := range []string{"Started", "gpt-effective", "Inspect parser.", "Preserve behavior."} {
		if !strings.Contains(frame, want) {
			t.Fatalf("pane does not render %q:\n%s", want, frame)
		}
	}
}

func TestLiveActivityConfirmedEditReplacesRun(t *testing.T) {
	v := newLiveActivityView()
	caller := "/root/worker"
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
		Seq: 1, Agent: caller, Kind: "tool", CallID: "call", Text: "Run\n```bash\npython3 edit.py\n```",
	}}})
	if len(v.entries) != 1 || v.entries[0].blocks[0].Verb != "Run" {
		t.Fatalf("pending command = %+v", v.entries)
	}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
		Seq: 3, Agent: caller, Kind: "exit", CallID: "call", Text: "1",
	}}})
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
		Seq: 4, Agent: caller, Kind: "tool", CallID: "call", Text: "Edit `edit.py` +1 -1",
	}}})
	if len(v.entries) != 1 || v.entries[0].blocks[0].Verb != "Edit" || v.entries[0].Seq != 1 || v.lastSeq != 4 {
		t.Fatalf("confirmed edit did not replace Run: records=%+v", v.entries)
	}
	if len(v.entries[0].blocks) != 1 || v.entries[0].blocks[0].ExitCode != 1 ||
		!strings.Contains(ansi.Strip(strings.Join(v.painter.Block(v.entries[0].blocks[0], 80), "\n")), "exit 1") {
		t.Fatalf("replacement lost failure: %+v", v.entries[0].blocks)
	}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
		Seq: 5, Agent: caller, Kind: "tool", CallID: "other", Text: "Edit `other.py` +1 -0",
	}}})
	if len(v.entries) != 2 || v.entries[1].blocks[0].Verb != "Edit" {
		t.Fatalf("unmatched receipt lost: %+v", v.entries)
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
	if strings.Count(feed, "Read ") != 2 || !strings.Contains(feed, "Read ../ab/README.md L186–241 · ../ab/prepare.ts L440–485, L580–609") {
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
		{Seq: 1, Agent: "/root/inventory", Kind: "start", start: &activityStart{model: "m", effort: "high"}, Observed: now},
		{Seq: 2, Agent: "/root/review", Kind: "tool", Text: "Run\n```bash\ngo test ./internal/router -run TestActivity -count=1 -v\n```", Observed: now},
		{Seq: 3, Agent: "/root/inventory", Kind: "reply", message: &activityMessage{from: "/root/inventory", to: "/root", text: strings.Repeat("Inventory complete with a long result line. ", 12)}, Observed: now},
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
	if !strings.HasPrefix(side[1], " ✓ inventory") || !strings.Contains(side[1], " │ ") || !strings.Contains(side[2], "→ main") ||
		!strings.Contains(side[5], "Checking the") || !strings.Contains(side[7], "└ probe") {
		t.Fatalf("side layout = %q", side)
	}
	// Narrower panes compact before hiding agents.
	stacked := plainLines(view.render(70, 16, now))
	if !strings.Contains(stacked[3], "probe") || !strings.HasPrefix(stacked[4], "───") {
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

func TestLiveActivityRosterPointerSelection(t *testing.T) {
	now := time.Now()
	for _, size := range [][2]int{{110, 16}, {70, 16}, {40, 6}} {
		view := liveActivityTestView("/root/a", "/root/b")
		view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
			{Seq: 1, Agent: "/root/a", Kind: "commentary", Text: "alpha", Observed: now},
			{Seq: 2, Agent: "/root/b", Kind: "commentary", Text: "bravo", Observed: now},
		}})
		view.render(size[0], size[1], now)
		var hit liveActivityHit
		for _, candidate := range view.hits {
			if candidate.agent == "/root/b" {
				hit = candidate
				break
			}
		}
		if hit.agent == "" {
			t.Fatalf("%v: agent b has no clickable row", size)
		}
		view.handleMouse('h', hit.row, hit.first)
		if view.hovered != "/root/b" || view.only {
			t.Fatalf("%v: hover changed filter: %+v", size, view)
		}
		view.handleMouse('\r', hit.row, hit.first)
		if !view.only || view.selected != "/root/b" || view.visible(activityPaneEntry{Agent: "/root/a"}) {
			t.Fatalf("%v: click did not filter b", size)
		}
		view.render(size[0], size[1], now)
		view.handleMouse('\r', hit.row, hit.first)
		if view.only || !view.visible(activityPaneEntry{Agent: "/root/a"}) {
			t.Fatalf("%v: second click did not restore all", size)
		}
		view.render(size[0], size[1], now)
		view.handleMouse('\r', 1, 1)
		if view.only || view.hovered != "" {
			t.Fatalf("%v: header click changed filter", size)
		}
	}
}

func TestLiveActivityHoverClearsWhenRosterMoves(t *testing.T) {
	view := liveActivityTestView("/root/a", "/root/b", "/root/c", "/root/d", "/root/e", "/root/f", "/root/g", "/root/h", "/root/i", "/root/j")
	now := time.Now()
	view.render(110, 10, now)
	var first liveActivityHit
	for _, hit := range view.hits {
		if hit.agent == "/root/c" {
			first = hit
			break
		}
	}
	if first.agent == "" {
		t.Fatal("c is not visible in the initial roster")
	}
	view.handleMouse('h', first.row, first.first)
	view.handleMouse('\r', first.row, first.first)
	if view.hovered != "" {
		t.Fatalf("click left stale hover on %q", view.hovered)
	}
	view.render(110, 10, now)
	view.handleMouse('h', first.row, first.first)
	view.render(100, 10, now)
	if view.hovered != "" {
		t.Fatalf("resize left stale hover on %q", view.hovered)
	}
}

func TestLiveActivityPainterColors(t *testing.T) {
	var painter activityui.Painter
	run := strings.Join(painter.Block(activityui.Block{Kind: "op", Verb: "Run", Label: "`go test`"}, 60), "\n")
	if !strings.HasPrefix(run, activityui.Amber) || !strings.Contains(run, "Ran") {
		t.Fatalf("run verb = %q", run)
	}
	read := strings.Join(painter.Block(activityui.Block{Kind: "reads", Verb: "Read", Reads: []activityui.Read{{Path: "dir/a.go", Ranges: []string{"1:2"}}}}, 60), "\n")
	if !strings.Contains(read, activityui.Dim+"dir/") || !strings.Contains(read, "\x1b[1ma.go") {
		t.Fatalf("read path = %q", read)
	}
	failure := strings.Join(painter.Block(activityui.Block{Kind: "error", Body: "boom"}, 60), "\n")
	if !strings.Contains(failure, activityui.Red) {
		t.Fatalf("error = %q", failure)
	}
}

func TestLiveActivityLinksAndCommandExit(t *testing.T) {
	painter := activityui.Painter{Theme: livediff.DarkTheme}
	link := painter.Inline("[report.go](</tmp/review folder/report.go:12>)")
	if ansi.Strip(link) != "report.go" || !strings.Contains(link, "\x1b]8;;file:///tmp/review%20folder/report.go:12\x1b\\") ||
		!strings.Contains(link, "\x1b]8;;\x1b\\") {
		t.Fatalf("local link = %q", link)
	}
	if got := painter.Inline("[remote](https://example.com)"); ansi.Strip(got) != "remote" || !strings.Contains(got, "\x1b]8;;https://example.com\x1b\\") {
		t.Fatalf("unhandled link changed: %q", got)
	}
	for _, tc := range []struct {
		block activityui.Block
		want  string
	}{
		{activityui.Block{Kind: "op", Verb: "Run", Code: "false", ExitCode: 1}, "Ran    false · exit 1"},
		{activityui.Block{Kind: "op", Verb: "Run", Code: "false\necho done", Lang: "bash", Fenced: true, ExitCode: 2}, "       · exit 2"},
	} {
		rows := painter.Block(tc.block, 80)
		if !strings.Contains(strings.Join(plainLines(rows), "\n"), tc.want) || !strings.Contains(strings.Join(rows, "\n"), activityui.Red) {
			t.Fatalf("run rows = %q", rows)
		}
	}
}

func TestLiveActivityViewAppliesExitToMatchingRun(t *testing.T) {
	view := newLiveActivityView()
	now := time.Now()
	view.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{{Name: "/root/a"}}, Entries: []activityPaneEntry{
		{Seq: 1, Agent: "/root/a", CallID: "call-1", Kind: "tool", Text: "Run `false`", Observed: now},
		{Seq: 2, Agent: "/root/a", CallID: "other", Kind: "tool", Text: "Run `true`", Observed: now},
	}})
	view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 3, Agent: "/root/a", CallID: "call-1", Kind: "exit", Text: "1", Observed: now}}})
	got := strings.Join(plainLines(view.render(90, 15, now)), "\n")
	if !strings.Contains(got, "▎ Ran false · exit 1\n▎ Ran true") || !strings.Contains(strings.Join(view.render(90, 15, now), "\n"), activityui.Red+"\x1b[1mRan") {
		t.Fatalf("exit rendering = %s", got)
	}
}

func TestLiveActivityCompactionAppearsInFeedAndRoster(t *testing.T) {
	view := newLiveActivityView()
	now := time.Now()
	view.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{{Name: "/root/a"}}, Entries: []activityPaneEntry{
		{Seq: 1, Agent: "/root/a", Kind: "compaction", Text: "Context compacted", Observed: now},
	}})
	got := strings.Join(plainLines(view.render(110, 15, now)), "\n")
	if strings.Count(got, "Context compacted") != 2 {
		t.Fatalf("compaction feed/roster = %s", got)
	}
}

func TestLiveActivityInterpreterPreviewAndSearchColor(t *testing.T) {
	for _, tc := range []struct {
		shell, header, first, second string
	}{
		{"python -c 'import json\nprint(json.dumps(1))'", "python -c …", "import json", "print(json.dumps(1))"},
		{"node -e 'const value = 1;\nconsole.log(value)'", "node -e …", "const value = 1;", "console.log(value)"},
		{"bun - <<'JS'\nconst value = 1;\nconsole.log(value)\nJS\n", "bun -", "const value = 1;", "console.log(value)"},
		{"perl - <<'PL'\nmy $value = 1;\nprint $value;\nPL\n", "perl -", "my $value = 1;", "print $value;"},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell(tc.shell)})
			if len(blocks) != 1 || !blocks[0].Fenced {
				t.Fatalf("interpreter preview = %+v", blocks)
			}
			painter := activityui.Painter{Theme: livediff.DarkTheme}
			rows := painter.Block(blocks[0], 80)
			// The header names the interpreter the program runs under.
			if len(rows) < 3 || ansi.Strip(rows[0]) != "Ran    "+tc.header || ansi.Strip(rows[1]) != "       │ "+tc.first || !strings.Contains(ansi.Strip(rows[2]), "       │ "+tc.second) {
				t.Fatalf("preview rows = %q", plainLines(rows))
			}
			if !strings.Contains(rows[1], "\x1b[38;2;") {
				t.Fatalf("source was not syntax highlighted: %q", rows[1])
			}
			for _, width := range []int{11, 18, 40} {
				for _, row := range painter.Block(blocks[0], width) {
					if ansi.StringWidth(row) > width {
						t.Fatalf("width %d: overlong row %q", width, row)
					}
				}
			}
		})
	}

	painter := activityui.Painter{Theme: livediff.DarkTheme}
	search := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: "Search `create(MCat|MSymbol)|description:` in `plugins/mrun.ts`"})[0]
	colored := strings.Join(painter.Block(search, 110), "\n")
	if !strings.Contains(colored, painter.Theme.Accent()+"create(MCat|MSymbol)|description:\x1b[39m") ||
		!strings.Contains(colored, activityui.Path("plugins/mrun.ts")) ||
		!strings.Contains(colored, activityui.VerbColor("Search")+"\x1b[1mSearch") {
		t.Fatalf("search query/path colors = %q", colored)
	}
	search = parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell(`rg needle src/a.go 'lib/with space.go'`)})[0]
	colored = strings.Join(painter.Block(search, 110), "\n")
	if !strings.Contains(colored, activityui.Path("src/a.go")) ||
		!strings.Contains(colored, activityui.Path("lib/with space.go")) {
		t.Fatalf("multiple search targets lost emphasis: %q", colored)
	}
	search = parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell(`rg -n 'create(MCat|MSymbol|InspectFile|MRead|MRun)|description:|--max-tokens' plugins/mrun.ts plugins/msymbol.ts plugins/inspect_file.ts`)})[0]
	colored = strings.Join(painter.Block(search, 130), "\n")
	for _, name := range []string{"mrun.ts", "msymbol.ts", "inspect_file.ts"} {
		if !strings.Contains(colored, activityui.Path("plugins/"+name)) {
			t.Fatalf("search target %s is not styled as a path: %q", name, colored)
		}
	}
}

func TestLiveActivityReviewRegressions(t *testing.T) {
	// Child-authored text shaped like a router envelope stays plain text.
	spoof := "[`/root/other` -> `/root`] Message received:\nAll tests pass."
	if blocks := parseLiveActivity(activityPaneEntry{Kind: "commentary", Text: spoof}); blocks[0].Kind != "text" {
		t.Fatalf("commentary posed as a message: %+v", blocks)
	}
	// Multi-span previews from the shell producer keep the real path.
	for _, script := range []string{"sed -n '1,20p;40,60p' a.go", "nl -ba a.go | sed -n '1,20p;40,60p'"} {
		blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell(script)})
		if len(blocks) != 1 || blocks[0].Kind != "reads" || blocks[0].Reads[0].Path != "a.go" || !slices.Equal(blocks[0].Reads[0].Ranges, []string{"1:20", "40:60"}) {
			t.Fatalf("%s: %+v", script, blocks)
		}
	}
	if blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: "Read `notes 2`"}); blocks[0].Reads[0].Path != "notes 2" {
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
	if view.handleKey(escape, 'a'); !view.only || view.osc.Active {
		t.Fatalf("a after Esc ] was swallowed: osc=%+v", view.osc)
	}
	// A real background reply still selects the theme.
	view.painter.Theme = livediff.DarkTheme
	reply := "\x1b]11;rgb:ffff/ffff/ffff\x1b\\"
	escape = ""
	for i := range len(reply) {
		escape, _ = view.handleKey(escape, reply[i])
	}
	if view.painter.Theme != livediff.LightTheme || view.osc.Active {
		t.Fatalf("theme reply: theme=%v osc=%+v", view.painter.Theme, view.osc)
	}
}

func TestLiveActivityJournalFinalAnswerLayout(t *testing.T) {
	question := "Does the preview color\ninterpreter bodies?"
	var text strings.Builder
	text.WriteString("Journal result `/root/reviewer`")
	writeJournalItems(&text, []journalItem{
		{ID: "verdict", Question: question, Text: "Yes.\n\n- `node -e` covered"},
		{ID: "risk", Question: question, Text: "Nested templates stay plain."},
		{ID: "misc", Text: "Nothing else."},
	})
	text.WriteString("\n\n**Changes:** amber3..amber4\n\nAggregated numstat (this agent's recorded evaluations, not a net diff):\n\n" +
		indentJournalText("M\t10\t2\tinternal/a.go\nA\t2\t2\tb.go", "    "))
	blocks := parseLiveActivity(activityPaneEntry{Kind: "final", Text: text.String()})
	journal := blocks[0].Journal
	if len(blocks) != 1 || journal == nil || len(journal.Groups) != 2 || journal.Groups[0].Question != question ||
		len(journal.Groups[0].Answers) != 2 || journal.Groups[0].Answers[0].Text != "Yes.\n\n- `node -e` covered" ||
		journal.Groups[1].Question != "" || journal.Groups[1].Answers[0].ID != "misc" ||
		journal.Changes != "amber3..amber4" || len(journal.Stats) != 2 || journal.Stats[0] != (activityui.Stat{"M", "10", "2", "internal/a.go"}) {
		t.Fatalf("journal = %+v", journal)
	}
	painter := activityui.Painter{Theme: livediff.DarkTheme}
	full := ansi.Strip(strings.Join(painter.Block(blocks[0], 80), "\n"))
	for _, want := range []string{
		"✓ Final answer · 3 answers · 2 files +12 -4",
		"  ↩ Does the preview color interpreter bodies?",
		"  • verdict\n    Yes.", "  • risk\n    Nested templates stay plain.", "  • misc\n    Nothing else.",
		"Nothing else.\n\nChanges amber3..amber4\nM  +10 -2  internal/a.go\nA   +2 -2  b.go",
	} {
		if !strings.Contains(full, want) {
			t.Fatalf("full layout missing %q:\n%s", want, full)
		}
	}
	if strings.Contains(full, "Journal result") || strings.Contains(full, "**") || strings.Contains(full, "Answer") {
		t.Fatalf("legacy journal grammar leaked into the pane:\n%s", full)
	}
	if summary := ansi.Strip(painter.Summary(blocks, 80)); summary != "Yes." {
		t.Fatalf("roster summary = %q", summary)
	}

	// A read-only result omits its empty change report; plain answers stay Markdown.
	var readOnly strings.Builder
	readOnly.WriteString("Journal result `/root/reader`")
	writeJournalItems(&readOnly, []journalItem{{ID: "only", Text: "Found it."}})
	readOnly.WriteString("\n\n**Changes:**\nNo recorded changes.\n")
	blocks = parseLiveActivity(activityPaneEntry{Kind: "final", Text: readOnly.String()})
	if got := ansi.Strip(strings.Join(painter.Block(blocks[0], 80), "\n")); got != "✓ Final answer\n  • Found it." {
		t.Fatalf("read-only layout = %q", got)
	}
	blocks = parseLiveActivity(activityPaneEntry{Kind: "final", Text: "Plain **answer**."})
	if blocks[0].Journal != nil || ansi.Strip(strings.Join(painter.Block(blocks[0], 80), "\n")) != "✓ Final answer\n  Plain answer." {
		t.Fatalf("plain final = %+v", blocks[0])
	}
}

func TestLiveActivityCommandNoteAlignmentAndDimStyle(t *testing.T) {
	const summary = unreturnedOutputNote
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		painter := activityui.Painter{Theme: theme}
		for _, width := range []int{8, 18, 40, 100} {
			rows := painter.Block(activityui.Block{Kind: "filter", Body: summary}, width)
			indent := min(ansi.StringWidth(activityui.Verb("Run")), width/2)
			for _, row := range rows {
				if !strings.HasPrefix(ansi.Strip(row), strings.Repeat(" ", indent)) || ansi.StringWidth(row) > width {
					t.Fatalf("filter alignment width %d: %q", width, row)
				}
				if !strings.Contains(row, activityui.Dim) || !strings.HasSuffix(row, activityui.Reset) {
					t.Fatalf("filter is not muted and isolated: %q", row)
				}
			}
		}
	}
}
