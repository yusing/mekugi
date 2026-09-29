package router

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
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
					u.applyObservedActivity()
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
				u.applyObservedActivity()
				if len(view.entries) != 1 || len(view.blocks[0]) != 2 || view.blocks[0][0].Verb != "Search" || view.blocks[0][1].Kind != "filter" {
					t.Fatalf("filter lifecycle: entries=%+v blocks=%+v", view.entries, view.blocks)
				}
				rows := strings.Join(view.painter.Block(view.blocks[0][1], 100), "\n")
				if !strings.Contains(rows, activityui.Dim) || !strings.Contains(ansi.Strip(rows), "−5/10 lines") {
					t.Fatalf("filter style: %q", rows)
				}
				if len(activity.takeNativeActivity("other")) != 0 {
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
			if len(u.view.blocks) != 1 || len(u.view.blocks[0]) != 1 || u.view.blocks[0][0].Results == nil || *u.view.blocks[0][0].Results != tc.want {
				t.Fatalf("lost count: %+v", u.view.blocks)
			}
			painted := strings.Join(u.view.painter.Block(u.view.blocks[0][0], 120), "\n")
			if !strings.Contains(painted, activityui.ResultCount(count)) {
				t.Fatalf("not muted: %q", painted)
			}
			restored := newAppServerSessionTestUI(t, t.TempDir())
			restored.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
			if *restored.view.blocks[0][0].Results != tc.want {
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
	if len(u.view.blocks) != 1 || u.view.blocks[0][0].Results != nil {
		t.Fatal("started search count")
	}
	item["results"] = []any{map[string]any{"title": "one"}, map[string]any{"title": "two"}}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	if len(u.view.entries) != 1 || u.view.blocks[0][0].Results == nil || *u.view.blocks[0][0].Results != 2 {
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
	u.applyObservedActivity()
	if len(u.view.entries) != 2 || len(u.view.blocks[1]) != 1 || u.view.blocks[1][0].Kind != "filter" {
		t.Fatalf("invented command for unmatched Code Mode filter: %+v", u.view.blocks)
	}
}

func TestAppServerFailedCodeModeCellShowsItsError(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "child"}[child], func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.view.conversation = true
			activity := newSubagentActivity()
			activity.attachNativePane("main")
			proxy := &mekugiProxy{activity: activity}
			u.proxy = proxy
			thread, view := "main", u.view
			if child {
				thread, view = "child", u.agents
				activity.observe("child", "main", "/root/worker", true)
				u.session.registerThread(appServerThreadInfo{ID: "child", AgentNickname: "worker"})
			}
			failed := `[{"type":"input_text","text":"Script failed\nWall time 0.0 seconds\nOutput:\n"},{"type":"input_text","text":"Script error:\nSyntaxError: Unexpected token '<<'\n    at cell:1"}]`
			completed := `[{"type":"input_text","text":"Script completed\nWall time 0.1 seconds\nOutput:\n"},{"type":"input_text","text":"Script error:\nprinted by the program"}]`
			input := `[{"type":"custom_tool_call","call_id":"bad","name":"exec","input":"python3 - << 'PY'"},` +
				`{"type":"custom_tool_call","call_id":"ok","name":"exec","input":"text('x')"},` +
				`{"type":"custom_tool_call_output","call_id":"bad","output":` + failed + `},` +
				`{"type":"custom_tool_call_output","call_id":"ok","output":` + completed + `}]`
			request := &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": json.RawMessage(input)}}
			// A repeated request carries the same output again; it is still one row.
			for range 2 {
				proxy.observeCodeModeFailures(thread, "exec", request, nil)
				u.applyObservedActivity()
			}
			if len(view.entries) != 1 || view.entries[0].Kind != "error" ||
				view.entries[0].Text != "Code Mode script failed: SyntaxError: Unexpected token '<<'" {
				t.Fatalf("failed cell entries: %+v", view.entries)
			}
			if !child {
				got := ansi.Strip(strings.Join(view.renderFeed(90, 40).lines, "\n"))
				if !strings.Contains(got, "✗ Main") || !strings.Contains(got, "│ Code Mode script failed: SyntaxError: Unexpected token '<<'") {
					t.Fatalf("Main lacks the failed cell:\n%s", got)
				}
			}
		})
	}
}

// A cell that printed nothing returned none of its commands' output; the rows
// say so rather than implying the model saw it.
func TestAppServerUnreturnedCodeModeOutputIsNoted(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	activity := newSubagentActivity()
	activity.attachNativePane("main")
	trace := newNativeTraceFixture(t)
	proxy := &mekugiProxy{activity: activity, nativeTrace: &nativeToolTrace{directory: trace.root}}
	u.proxy = proxy
	cells := []struct{ call, source, output string }{
		{"silent", `await tools.exec_command({cmd: "git log"}); await tools.exec_command({cmd: "true"})`, `"Script completed\nWall time 0.1 seconds\nOutput:"`},
		{"printed", `text(await tools.exec_command({cmd: "go version"}))`, `"Script completed\nWall time 0.1 seconds\nOutput:\nclean"`},
	}
	commands := []struct{ id, cmd string }{{"log-exec", "git log"}, {"quiet-exec", "true"}, {"status-exec", "go version"}}
	var input []string
	for _, cell := range cells {
		trace.start("main", cell.call+"-runtime", cell.call, cell.source)
		for _, command := range commands {
			if strings.Contains(cell.source, `"`+command.cmd+`"`) {
				trace.tool("main", cell.call+"-runtime", command.id, "exec_command", string(mustMarshalJSON(map[string]any{"cmd": command.cmd})))
				trace.result("main", command.id, "completed", map[string]any{"exit_code": 0})
			}
		}
		trace.end("main", cell.call+"-runtime")
		input = append(input, string(mustMarshalJSON(map[string]any{"type": "custom_tool_call", "call_id": cell.call, "name": "exec", "input": cell.source})))
	}
	for _, cell := range cells {
		input = append(input, `{"type":"custom_tool_call_output","call_id":"`+cell.call+`","output":`+cell.output+`}`)
	}
	for _, command := range commands {
		output := "commit abc\n"
		if command.id == "quiet-exec" {
			output = ""
		}
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
			"id": command.id, "type": "commandExecution", "command": command.cmd, "status": "completed", "exitCode": 0, "aggregatedOutput": output}})
	}
	rollCommandOutput(u)
	request := &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": json.RawMessage("[" + strings.Join(input, ",") + "]")}}
	for range 2 { // A repeated request notes each command once.
		proxy.observeCodeModeFailures("main", "exec", request, nil)
		u.applyObservedActivity()
	}
	got := ansi.Strip(strings.Join(u.view.renderFeed(90, 60).lines, "\n"))
	under := "Ran git log\n│     ┆ commit abc\n│     " + unreturnedOutputNote + "\n"
	if strings.Count(got, unreturnedOutputNote) != 1 || !strings.Contains(got, under) {
		t.Fatalf("want one note, under git log's output:\n%s", got)
	}
}

func TestCodeModeReturnedNothing(t *testing.T) {
	header := "Script completed\nWall time 0.1 seconds\nOutput:\n"
	for raw, want := range map[string]bool{
		string(mustMarshalJSON(header)):                                                                                                             true,
		string(mustMarshalJSON(strings.TrimSuffix(header, "\n"))):                                                                                   true,
		string(mustMarshalJSON(header + "  \n")):                                                                                                    true,
		string(mustMarshalJSON(header + "printed")):                                                                                                 false,
		`[{"type":"input_text","text":` + string(mustMarshalJSON(header)) + `},{"type":"input_text","text":""}]`:                                    true,
		`[{"type":"input_text","text":` + string(mustMarshalJSON(header)) + `},{"type":"input_text","text":"x"}]`:                                   false,
		`[{"type":"input_text","text":` + string(mustMarshalJSON(header)) + `},{"type":"input_image","image_url":""}]`:                              false,
		`[{"type":"input_text","text":"Script failed\nWall time 0.0 seconds\nOutput:\n"},{"type":"input_text","text":"Script error:\nTypeError"}]`:  true,
		`[{"type":"input_text","text":"Script completed\nWall time 0.0 seconds\nOutput:\n"},{"type":"input_text","text":"Script error:\nprinted"}]`: false,
		string(mustMarshalJSON("Script running with cell ID 7\nWall time 10.0 seconds\nOutput:\n")):                                                 false,
	} {
		if got := codeModeReturnedNothing(json.RawMessage(raw)); got != want {
			t.Errorf("codeModeReturnedNothing(%s) = %v, want %v", raw, got, want)
		}
	}
}
