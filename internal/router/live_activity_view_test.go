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
	start := parseLiveActivity(activityPaneEntry{Kind: "start", Text: "Started · `m` high · tier `fast`"})
	if len(start) != 1 || start[0].kind != "start" || start[0].label != "`m` high · tier `fast`" || start[0].body != "" {
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
		Seq: 1, Agent: "/root/explorer", Kind: "start", Text: subagentStartCommentary(&request, "/root/explorer"), Observed: now,
	}}})
	view.only, view.selected = true, "/root/explorer"
	frame := strings.Join(plainLines(view.render(100, 20, now)), "\n")
	for _, want := range []string{"Started", "gpt-effective", "Spawn assignment:", "Inspect parser.", "Preserve behavior."} {
		if !strings.Contains(frame, want) {
			t.Fatalf("pane does not render %q:\n%s", want, frame)
		}
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
		{Seq: 1, Agent: "/root/inventory", Kind: "start", Text: "Started · `m` high", Observed: now},
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
		if hit.agent == "/root/f" {
			first = hit
			break
		}
	}
	if first.agent == "" {
		t.Fatal("f is not visible in the initial roster")
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

func TestLiveActivityLinksAndCommandExit(t *testing.T) {
	painter := liveActivityPainter{theme: livediff.DarkTheme}
	link := painter.inline("[report.go](</tmp/review folder/report.go:12>)")
	if ansi.Strip(link) != "report.go" || !strings.Contains(link, "\x1b]8;;file:///tmp/review%20folder/report.go:12\x1b\\") ||
		!strings.Contains(link, "\x1b]8;;\x1b\\") {
		t.Fatalf("local link = %q", link)
	}
	if got := painter.inline("[remote](https://example.com)"); got != "[remote](https://example.com)" {
		t.Fatalf("unhandled link changed: %q", got)
	}
	for _, tc := range []struct {
		block liveActivityBlock
		want  string
	}{
		{liveActivityBlock{kind: "op", verb: "Run", code: "false", exitCode: 1}, "Run    false (exit 1)"},
		{liveActivityBlock{kind: "op", verb: "Run", code: "false\necho done", lang: "bash", fenced: true, exitCode: 2}, "       (exit 2)"},
	} {
		rows := painter.block(tc.block, 80)
		if !strings.Contains(strings.Join(plainLines(rows), "\n"), tc.want) || !strings.Contains(strings.Join(rows, "\n"), liveActivityRed) {
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
	if !strings.Contains(got, "false (exit 1)") || strings.Contains(got, "true (exit 1)") || strings.Count(got, "▎ Run") != 2 {
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
		shell, first, second string
	}{
		{"python -c 'import json\nprint(json.dumps(1))'", "import json", "print(json.dumps(1))"},
		{"node -e 'const value = 1;\nconsole.log(value)'", "const value = 1;", "console.log(value)"},
		{"bun - <<'JS'\nconst value = 1;\nconsole.log(value)\nJS\n", "const value = 1;", "console.log(value)"},
		{"perl - <<'PL'\nmy $value = 1;\nprint $value;\nPL\n", "my $value = 1;", "print $value;"},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell(tc.shell)})
			if len(blocks) != 1 || !blocks[0].fenced {
				t.Fatalf("interpreter preview = %+v", blocks)
			}
			painter := liveActivityPainter{theme: livediff.DarkTheme}
			rows := painter.block(blocks[0], 80)
			if len(rows) < 2 || !strings.HasPrefix(ansi.Strip(rows[0]), "Run    │ "+tc.first) || !strings.Contains(ansi.Strip(rows[1]), "│ "+tc.second) {
				t.Fatalf("preview rows = %q", plainLines(rows))
			}
			if !strings.Contains(rows[0], "\x1b[38;2;") {
				t.Fatalf("source was not syntax highlighted: %q", rows[0])
			}
			for _, width := range []int{11, 18, 40} {
				for _, row := range painter.block(blocks[0], width) {
					if ansi.StringWidth(row) > width {
						t.Fatalf("width %d: overlong row %q", width, row)
					}
				}
			}
		})
	}

	painter := liveActivityPainter{theme: livediff.DarkTheme}
	search := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: "Search `create(MCat|MSymbol)|description:` in `plugins/mrun.ts`"})[0]
	colored := strings.Join(painter.block(search, 110), "\n")
	if !strings.Contains(colored, painter.theme.Accent()+"create(MCat|MSymbol)|description:\x1b[39m") ||
		!strings.Contains(colored, liveActivityVerbColor("Search")+liveActivityDim+"plugins/"+liveActivityUndim+"\x1b[1mmrun.ts") ||
		!strings.HasPrefix(colored, liveActivityVerbColor("Search")) {
		t.Fatalf("search query/path colors = %q", colored)
	}
	search = parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell(`rg needle src/a.go 'lib/with space.go'`)})[0]
	colored = strings.Join(painter.block(search, 110), "\n")
	if !strings.Contains(colored, liveActivityVerbColor("Search")+liveActivityDim+"src/"+liveActivityUndim+"\x1b[1ma.go") ||
		!strings.Contains(colored, liveActivityVerbColor("Search")+liveActivityDim+"lib/"+liveActivityUndim+"\x1b[1mwith space.go") {
		t.Fatalf("multiple search targets lost emphasis: %q", colored)
	}
	search = parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell(`rg -n 'create(MCat|MSymbol|InspectFile|MRead|MRun)|description:|--max-tokens' plugins/mrun.ts plugins/msymbol.ts plugins/inspect_file.ts`)})[0]
	colored = strings.Join(painter.block(search, 130), "\n")
	for _, name := range []string{"mrun.ts", "msymbol.ts", "inspect_file.ts"} {
		if !strings.Contains(colored, liveActivityVerbColor("Search")+liveActivityDim+"plugins/"+liveActivityUndim+"\x1b[1m"+name) {
			t.Fatalf("search target %s is not purple: %q", name, colored)
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
	if view.handleKey(escape, 'o'); !view.only || view.osc.Active {
		t.Fatalf("o after Esc ] was swallowed: osc=%+v", view.osc)
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
		indentJournalText("10\t2\tinternal/a.go\n2\t2\tb.go", "    "))
	blocks := parseLiveActivity(activityPaneEntry{Kind: "final", Text: text.String()})
	journal := blocks[0].journal
	if len(blocks) != 1 || journal == nil || len(journal.groups) != 2 || journal.groups[0].question != question ||
		len(journal.groups[0].answers) != 2 || journal.groups[0].answers[0].text != "Yes.\n\n- `node -e` covered" ||
		journal.groups[1].question != "" || journal.groups[1].answers[0].id != "misc" ||
		journal.changes != "amber3..amber4" || len(journal.stats) != 2 || journal.stats[0] != (liveActivityStat{"10", "2", "internal/a.go"}) {
		t.Fatalf("journal = %+v", journal)
	}
	painter := liveActivityPainter{theme: livediff.DarkTheme}
	render := func(compact bool) string {
		block := blocks[0]
		block.compact = compact
		return ansi.Strip(strings.Join(painter.block(block, 80), "\n"))
	}
	full := render(false)
	for _, want := range []string{
		"✓ Final answer · 3 answers · 2 files +12 -4",
		"  Q Does the preview color\n    interpreter bodies?",
		"  A verdict\n    Yes.", "  A risk\n    Nested templates stay plain.", "  • misc\n    Nothing else.",
		"  Changes amber3..amber4\n    +10 -2 internal/a.go\n     +2 -2 b.go",
	} {
		if !strings.Contains(full, want) {
			t.Fatalf("full layout missing %q:\n%s", want, full)
		}
	}
	if strings.Contains(full, "Journal result") || strings.Contains(full, "**") || strings.Contains(full, "Answer") {
		t.Fatalf("legacy journal grammar leaked into the pane:\n%s", full)
	}
	if compact := render(true); !strings.Contains(compact, "  Q Does the preview color interpreter bodies?\n  A verdict") {
		t.Fatalf("shared view kept a multi-row question:\n%s", compact)
	}
	if summary := ansi.Strip(painter.summary(blocks)); summary != "Yes." {
		t.Fatalf("roster summary = %q", summary)
	}

	// A read-only result omits its empty change report; plain answers stay Markdown.
	var readOnly strings.Builder
	readOnly.WriteString("Journal result `/root/reader`")
	writeJournalItems(&readOnly, []journalItem{{ID: "only", Text: "Found it."}})
	readOnly.WriteString("\n\n**Changes:**\nNo recorded changes.\n")
	blocks = parseLiveActivity(activityPaneEntry{Kind: "final", Text: readOnly.String()})
	if got := ansi.Strip(strings.Join(painter.block(blocks[0], 80), "\n")); got != "✓ Final answer · 1 answer\n  • Found it." {
		t.Fatalf("read-only layout = %q", got)
	}
	blocks = parseLiveActivity(activityPaneEntry{Kind: "final", Text: "Plain **answer**."})
	if blocks[0].journal != nil || ansi.Strip(strings.Join(painter.block(blocks[0], 80), "\n")) != "✓ Final answer\n  Plain answer." {
		t.Fatalf("plain final = %+v", blocks[0])
	}
}

func TestLiveActivityFilterAlignmentAndDimStyle(t *testing.T) {
	const summary = "~tokens 3.5K→2.4K (-29.4%) · -42/104 lines · 0.6s"
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		painter := liveActivityPainter{theme: theme}
		for _, width := range []int{8, 18, 40, 100} {
			rows := painter.block(liveActivityBlock{kind: "filter", body: summary}, width)
			indent := min(ansi.StringWidth(liveActivityVerb("Run")), width/2)
			for _, row := range rows {
				if !strings.HasPrefix(ansi.Strip(row), strings.Repeat(" ", indent)) || ansi.StringWidth(row) > width {
					t.Fatalf("filter alignment width %d: %q", width, row)
				}
				if !strings.Contains(row, liveActivityDim) || !strings.Contains(row, "\x1b[38;2;") || !strings.HasSuffix(row, liveActivityReset) {
					t.Fatalf("filter is not muted and isolated: %q", row)
				}
			}
		}
	}
}
