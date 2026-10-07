package router

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestRequestedEditGroupsMergeAdjacentInvocations(t *testing.T) {
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
		if strings.Count(got, "Edited  a.go") != 1 || strings.Count(got, "via python3 ×2") != 1 || strings.Count(got, "Edit    c.go") != 1 || strings.Count(got, "· failed") != 1 || strings.Contains(got, "apply_patch") || strings.Contains(got, " · python3") {
			t.Fatalf("%s invocation grouping:\n%s", agent, got)
		}
		// Both navigable views keep each invocation's rows, even for repeated paths.
		for _, row := range []string{"a.go  +2 -1", "b.go  +3"} {
			if count := strings.Count(got, row); count != 2 {
				t.Fatalf("%s shows %q %d times:\n%s", agent, row, count, got)
			}
		}
		// The failed group's first row names the outcome for both rows.
		for _, want := range []string{"c.go  +1 -1 · failed", "        d.go  +1"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s lost %q:\n%s", agent, want, got)
			}
		}
	}
}

func TestUISnapshotUnresolvedEditCommand(t *testing.T) {
	const command = "python3 - <<'PY'\nfrom pathlib import Path\nname='a'\nname += '.txt'\n(Path('src') / name).write_text('new')\nPY"
	for _, partial := range []bool{false, true} {
		command := command
		if partial {
			command = strings.Replace(command, "name='a'", "Path('known.go').write_text('x')\nname='a'", 1)
		}
		for _, tracked := range []bool{false, true} {
			t.Run(fmt.Sprintf("partial=%v/tracked=%v", partial, tracked), func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				u.view.conversation = true
				u.view.painter.Theme = livediff.DarkTheme
				now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
				u.view.clock = func() time.Time { return now }
				native := &liveActivityNativeItem{thread: "main", item: "cmd", command: command, status: "completed"}
				if tracked {
					native.segments = []commandSegment{{source: command, text: execSegmentText(command)}}
				}
				u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
					Seq: 1, Agent: "Main", Kind: "tool", CallID: "cmd", Text: toolActivityShell(command), Observed: now, native: native,
				}}})
				feed := u.view.renderFeed(100, 40)
				var opened bool
				for _, snippet := range feed.snippets {
					if u.shell.openOutput(u.view, snippet) {
						opened = true
						break
					}
				}
				if !opened || len(u.shell.output.pages) != 1 || u.shell.output.pages[0].EditSource != "" {
					t.Fatal("unresolved edit has no ordinary command dialog")
				}
				// The interpreter projection is the original source, not a made-up edit.
				page := u.view.painter.DialogPage(u.shell.output.pages[0], 100)
				var source string
				for _, line := range page.Lines {
					source += ansi.Strip(line.Text) + "\n"
				}
				if !strings.Contains(source, "name += '.txt'") || !strings.Contains(source, ".write_text('new')") {
					t.Fatalf("dialog lost original source: %q", source)
				}
				snapshot := "unresolved-edit-command"
				if partial {
					snapshot = "partial-edit-command"
					if tracked {
						snapshot += "-ran"
					}
				}
				assertNativeUISnapshot(t, snapshot, feed.lines)
			})
		}
	}
}

func TestRequestedReasoningHeadsOperationTree(t *testing.T) {
	for _, agent := range []string{"Main", "/root/worker"} {
		for _, operation := range []struct{ text, first string }{
			{"Read `a.go`\n\nSearch `needle`", "├ Read   a.go"},
			{"Edit `a.go` +2 -1 · git stash push\n\nEdit `b.go` +1 -0 · git stash push", "└ Edited a.go"},
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
			plain := plainLines(feed.lines)
			row := slices.IndexFunc(plain, func(row string) bool { return strings.Contains(row, "Locating targeted files") })
			// The operations branch from the reasoning row with no gap between.
			if row < 0 || row+1 >= len(plain) || !strings.Contains(plain[row+1], operation.first) || strings.Count(strings.Join(plain, "\n"), "Locating targeted files") != 1 {
				t.Fatalf("%s reasoning does not head its operations:\n%s", agent, strings.Join(plain, "\n"))
			}
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
	view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 3, Agent: "/root/a", Kind: "reply", message: &activityMessage{from: "/root/a", to: "/root", text: "Answer"}, Observed: now}}})
	if summary, _ := view.current(view.agents[0], now); !strings.Contains(ansi.Strip(summary), "← a") {
		t.Fatalf("newer addressed message missing: %q", summary)
	}
}

func TestRequestedRosterMetricSlotsStableAcrossAvailabilityAndFormat(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	view := liveActivityTestView("/root/a")
	view.agents[0].Started = now.Add(-52 * time.Second)
	view.agents[0].WorkTimer = activeWorkTimer{Known: true, ElapsedNS: int64(52 * time.Second)}
	view.agents[0].LastResponse = now
	view.agents[0].InputTokens, view.agents[0].OutputTokens = 140_600, 789
	view.agents[0].Turns, view.agents[0].Roundtrips, view.agents[0].Cost, view.agents[0].CostKnown = 7, 7, .5171, true
	full := plainLines(view.metricTable(view.roster(), now))[0]
	view.agents[0].InputTokens, view.agents[0].OutputTokens = 73_700, 844
	view.agents[0].Turns, view.agents[0].Roundtrips, view.agents[0].Cost = 14, 14, 12.0811
	view.agents[0].LastResponse = now.Add(-4 * time.Second)
	changed := plainLines(view.metricTable(view.roster(), now))[0]
	view.agents[0].InputTokens, view.agents[0].OutputTokens = 0, 0
	view.agents[0].Turns, view.agents[0].Roundtrips, view.agents[0].CostKnown = 0, 0, false
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
