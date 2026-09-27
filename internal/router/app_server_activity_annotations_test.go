package router

import (
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
)

func TestAppServerNativeFilterActivity(t *testing.T) {
	for _, child := range []bool{false, true} {
		for _, timing := range []string{"before", "during", "after"} {
			t.Run(timing+map[bool]string{false: "/main", true: "/child"}[child], func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				activity := newSubagentActivity()
				activity.attachNativePane("main")
				activity.observe("other", "", "/root", false)
				u.proxy = &mekugiProxy{activity: activity}
				thread := "main"
				view := u.view
				if child {
					thread, view = "child", u.agents
					activity.observe("child", "main", "/root/worker", true)
					u.session.registerThread(appServerThreadInfo{ID: "child", AgentNickname: "worker"})
				}
				item := appServerItem{ID: "cmd", Type: "commandExecution", Command: "rg needle src"}
				publish := func() {
					event := exploreFilterEvent{Command: item.Command, LinesBefore: 10, LinesRemoved: 5}
					activity.collectEvent(activityEvent{thread: thread, source: "filter", kind: "output_filter", callID: "cmd", text: event.text(), filter: &event})
					u.applyFilterActivity()
				}
				if timing == "before" {
					publish()
				}
				appServerTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "t", "item": item})
				if timing == "during" {
					publish()
				}
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": item})
				if timing == "after" {
					publish()
				}
				u.applyFilterActivity()
				if len(view.entries) != 1 || len(view.blocks[0]) != 2 || view.blocks[0][0].verb != "Search" || view.blocks[0][1].kind != "filter" {
					t.Fatalf("filter lifecycle: entries=%+v blocks=%+v", view.entries, view.blocks)
				}
				rows := strings.Join(view.painter.block(view.blocks[0][1], 100), "\n")
				if !strings.Contains(rows, liveActivityDim) || !strings.Contains(ansi.Strip(rows), "−5/10 lines") {
					t.Fatalf("filter style: %q", rows)
				}
				if len(activity.takeNativeFilters("other")) != 0 {
					t.Fatal("cross-thread annotations")
				}
			})
		}
	}
}

func TestAppServerSearchResultCounts(t *testing.T) {
	for _, tc := range []struct {
		command, output string
		exit            int
		want            int
	}{
		{"rg -n needle src", "a.go:1:needle\nb.go:2:needle\n", 0, 2},
		{"grep -n needle a.go", "1:needle\n2:needle\n", 0, 2},
		{"rg needle", "", 1, 0},
		{"rg -c needle", "a.go:2\nb.go:3\n", 0, 5},
		{"rg -n -e needle --glob '*.go' src", "a.go:1:needle\n", 0, 1},
		{"rg -A 1 needle", "a.go:1:needle\na.go-2-context\n", 0, -1},
		{"rg -q needle", "", 0, -1},
		{"rg -r 'a\nb' needle", "a\nb\n", 0, -1},
		{"rg -cl needle 123", "123\n", 0, -1},
		{"rg needle", "one\n[100 bytes omitted]\ntwo\n", 0, -1},
		{"rg needle", "error\n", 2, -1},
		{"rg needle", "Output truncated\na.go:1:needle\n", 0, -1},
		{"rg needle | head -1", "a.go:1:needle\n", 0, -1},
		{"rg needle; echo extra", "a.go:1:needle\nextra\n", 0, -1},
	} {
		t.Run(tc.command+"/"+tc.output, func(t *testing.T) {
			item := appServerItem{Type: "commandExecution", ID: "s", Command: tc.command, Status: "completed", ExitCode: new(tc.exit), AggregatedOutput: new(tc.output)}
			count := appServerSearchResults(item)
			if tc.want < 0 {
				if count != nil {
					t.Fatalf("invented count %d", *count)
				}
				return
			}
			if count == nil || *count != tc.want {
				t.Fatalf("count %v, want %d", count, tc.want)
			}
			u := newAppServerSessionTestUI(t, t.TempDir())
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
			if len(u.view.blocks) != 1 || len(u.view.blocks[0]) != 1 || u.view.blocks[0][0].results == nil || *u.view.blocks[0][0].results != tc.want {
				t.Fatalf("lost count: %+v", u.view.blocks)
			}
			painted := strings.Join(u.view.painter.block(u.view.blocks[0][0], 120), "\n")
			if !strings.Contains(painted, liveActivityResultCount(count)) {
				t.Fatalf("not muted: %q", painted)
			}
			restored := newAppServerSessionTestUI(t, t.TempDir())
			restored.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
			if *restored.view.blocks[0][0].results != tc.want {
				t.Fatal("restored count lost")
			}
		})
	}
	item := appServerItem{Type: "commandExecution", Command: "rg needle", Status: "completed", ExitCode: new(0)}
	if appServerSearchResults(item) != nil {
		t.Fatal("missing output is not zero")
	}
	item.AggregatedOutput = new(strings.Repeat("x", 1<<20))
	if appServerSearchResults(item) != nil {
		t.Fatal("silent cap counted as complete output")
	}
	item.AggregatedOutput, item.ExitCode = nil, new(1)
	if count := appServerSearchResults(item); count == nil || *count != 0 {
		t.Fatal("host empty-output omission lost no-match result")
	}
}

func TestAppServerWebSearchResults(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	item := map[string]any{"id": "web", "type": "webSearch", "query": "golang"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	if len(u.view.blocks) != 1 || u.view.blocks[0][0].results != nil {
		t.Fatal("started search count")
	}
	item["results"] = []any{map[string]any{"title": "one"}, map[string]any{"title": "two"}}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	if len(u.view.entries) != 1 || u.view.blocks[0][0].results == nil || *u.view.blocks[0][0].results != 2 {
		t.Fatalf("completed search: %+v", u.view.blocks)
	}
}

func TestAppServerCodeModeFilterDoesNotInventCommand(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	activity := newSubagentActivity()
	activity.attachNativePane("main")
	u.proxy = &mekugiProxy{activity: activity}
	item := appServerItem{ID: "exec-nested", Type: "commandExecution", Command: "rg needle"}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	event := exploreFilterEvent{Command: item.Command, LinesBefore: 10, LinesRemoved: 5}
	activity.collectEvent(activityEvent{thread: "main", source: "filtered", kind: "output_filter", callID: "outer-exec", text: event.text(), filter: &event})
	u.applyFilterActivity()
	if len(u.view.entries) != 2 || len(u.view.blocks[1]) != 1 || u.view.blocks[1][0].kind != "filter" {
		t.Fatalf("invented command for unmatched Code Mode filter: %+v", u.view.blocks)
	}
}
