package launch

import (
	"encoding/json/jsontext"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotWebSearch(t *testing.T) {
	const query = "site:example.test/a `Go` | regexp.*"
	items := []binding.ThreadItem{
		{ID: "query", Type: "webSearch", Query: query, Results: []jsontext.Value{[]byte(`{"title":"one"}`), []byte(`{"title":"two"}`)}},
		{ID: "queries", Type: "webSearch", Action: &binding.WebSearchAction{Type: "search", Queries: []string{"first/path", "second/path"}}},
		{ID: "action", Type: "webSearch", Action: &binding.WebSearchAction{Type: "search", Query: "third/path"}},
		{ID: "open", Type: "webSearch", Action: &binding.WebSearchAction{Type: "openPage", URL: "https://example.test/a"}},
		{ID: "find", Type: "webSearch", Action: &binding.WebSearchAction{Type: "findInPage", Pattern: "needle.*", URL: "https://example.test/a"}},
		{ID: "local", Type: "commandExecution", Command: "rg 'needle.*' src", ExitCode: new(1)},
	}
	for i := range items {
		items[i].Status = "completed"
		if items[i].Type == "webSearch" && items[i].Results == nil {
			items[i].Results = []jsontext.Value{}
		}
	}
	for _, mode := range []string{"live", "restored", "replay"} {
		t.Run(mode, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			var blocks []activityui.Block
			switch mode {
			case "live":
				for _, item := range items {
					appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
					appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
				}
			case "restored":
				u.restoreHistory([]binding.HistoryTurn{{ID: "t", Status: "completed", Items: items}})
			case "replay":
				start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
				source := &playback.Source{Thread: "main", Cwd: "/workspace", Start: start, End: start.Add(time.Second), Threads: map[string]string{"main": "/root"}}
				for _, item := range items {
					source.Events = append(source.Events, playback.Event{At: start, Kind: "item/completed", ThreadID: "main", TurnID: "t", Item: item.Observation()})
				}
				p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
				t.Cleanup(p.close)
				if err := p.advance(time.Second); err != nil {
					t.Fatal(err)
				}
				for i := range items {
					blocks = append(blocks, p.ui.testActivity(p.ui.view).blocks(i)...)
				}
			}
			if mode != "replay" {
				for i := range items {
					blocks = append(blocks, u.testActivity(u.shell.Transcript).blocks(i)...)
				}
			}
			if len(blocks) != len(items) {
				t.Fatalf("blocks: %+v", blocks)
			}
			for i, want := range []string{"Web Search", "Web Search", "Web Search", "Open", "Search", "Search"} {
				if blocks[i].Verb != want {
					t.Fatalf("item %d: verb %q, want %q", i, blocks[i].Verb, want)
				}
			}
			if blocks[0].Results == nil || *blocks[0].Results != 2 {
				t.Fatal("web count lost")
			}
			for _, theme := range []diffrender.Theme{diffrender.TerminalTheme, diffrender.DarkTheme, diffrender.LightTheme} {
				p := activityui.Painter{Theme: theme}
				if got := ansi.Strip(strings.Join(p.Block(blocks[0], 100), "\n")); !strings.Contains(got, query) {
					t.Fatalf("literal query changed: %q", got)
				}
				if got := ansi.Strip(p.Summary(blocks[:1], 100)); !strings.Contains(got, query) {
					t.Fatalf("query summary changed: %q", got)
				}
				for _, width := range []int{40, 100} {
					var rows []string
					for _, block := range activityui.GroupOperations(blocks) {
						rows = append(rows, p.Block(block, width)...)
					}
					if batch, ok := activityui.CollapseBatch(blocks); ok {
						batchRows := p.Block(batch, width)
						if width == 100 && !strings.Contains(ansi.Strip(strings.Join(batchRows, "\n")), "Web Search ×3") {
							t.Fatalf("batch must count query sets, not queries: %q", batchRows)
						}
						rows = append(rows, batchRows...)
					} else {
						t.Fatal("search batch no longer collapses")
					}
					rows = append(rows, "plain following row")
					uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/web-search-%d-%d.txt", theme, width), rows, width)
				}
			}
		})
	}
}
