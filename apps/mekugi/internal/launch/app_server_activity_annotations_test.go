package launch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

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
			item := binding.ThreadItem{Type: "commandExecution", ID: "s", Command: tc.command, Status: "completed", ExitCode: new(tc.exit), AggregatedOutput: new(tc.output)}
			count := sessionpresentation.SearchResults(item.Observation())
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
			if len(u.attached.source.Entries) != 1 || len(u.testActivity(u.shell.Transcript).blocks(0)) != 1 || u.testActivity(u.shell.Transcript).blocks(0)[0].Results == nil || *u.testActivity(u.shell.Transcript).blocks(0)[0].Results != tc.want {
				t.Fatalf("lost count: %+v", u.attached.source.Entries)
			}
			painted := strings.Join(u.shell.Transcript.Painter.Block(u.testActivity(u.shell.Transcript).blocks(0)[0], 120), "\n")
			if !strings.Contains(painted, activityui.ResultCount(count)) {
				t.Fatalf("not muted: %q", painted)
			}
			restored := newAppServerSessionTestUI(t, t.TempDir())
			restored.restoreHistory([]binding.HistoryTurn{{ID: "t", Status: "completed", Items: []binding.ThreadItem{item}}})
			if *restored.testActivity(restored.shell.Transcript).blocks(0)[0].Results != tc.want {
				t.Fatal("restored count lost")
			}
		})
	}
	item := binding.ThreadItem{Type: "commandExecution", Command: "rg needle", Status: "completed", ExitCode: new(0)}
	if sessionpresentation.SearchResults(item.Observation()) != nil {
		t.Fatal("missing output is not zero")
	}
	item.AggregatedOutput = new(strings.Repeat("x", 1<<20))
	if sessionpresentation.SearchResults(item.Observation()) != nil {
		t.Fatal("silent cap counted as complete output")
	}
	item.AggregatedOutput, item.ExitCode = nil, new(1)
	if count := sessionpresentation.SearchResults(item.Observation()); count == nil || *count != 0 {
		t.Fatal("host empty-output omission lost no-match result")
	}
}

func TestAppServerWebSearchResults(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	item := map[string]any{"id": "web", "type": "webSearch", "query": "golang"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	if len(u.attached.source.Entries) != 1 || u.testActivity(u.shell.Transcript).blocks(0)[0].Results != nil {
		t.Fatal("started search count")
	}
	item["results"] = []any{map[string]any{"title": "one"}, map[string]any{"title": "two"}}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	if len(u.attached.source.Entries) != 1 || u.testActivity(u.shell.Transcript).blocks(0)[0].Results == nil || *u.testActivity(u.shell.Transcript).blocks(0)[0].Results != 2 {
		t.Fatalf("completed search: %+v", u.attached.source.Entries)
	}
}

func TestAppServerFailedCodeModeCellShowsItsError(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "child"}[child], func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.shell.Transcript.Conversation = true
			u.testActivity(u.shell.Transcript).testAudience()
			activity := sessioncontrol.NewAudience(nil)
			activity.Attach("main")
			proxy := &observationFixture{activity: activity}
			setTestAppServerIntegration(u, proxy)
			thread, view := "main", u.testActivity(u.shell.Transcript)
			if child {
				thread, view = "child", u.testActivity(u.shell.Agents)
				activity.Observe("child", "main", "/root/worker", true)
				registerThread(&u.attached.session, binding.ThreadInfo{ID: "child", AgentNickname: "worker"})
			}
			failed := `[{"type":"input_text","text":"Script failed\nWall time 0.0 seconds\nOutput:\n"},{"type":"input_text","text":"Script error:\nSyntaxError: Unexpected token '<<'\n    at cell:1"}]`
			completed := `[{"type":"input_text","text":"Script completed\nWall time 0.1 seconds\nOutput:\n"},{"type":"input_text","text":"Script error:\nprinted by the program"}]`
			input := `[{"type":"custom_tool_call","call_id":"bad","name":"exec","input":"python3 - << 'PY'"},` +
				`{"type":"custom_tool_call","call_id":"ok","name":"exec","input":"text('x')"},` +
				`{"type":"custom_tool_call_output","call_id":"bad","output":` + failed + `},` +
				`{"type":"custom_tool_call_output","call_id":"ok","output":` + completed + `}]`
			request := &codexresponses.Request{Fields: map[string]json.RawMessage{"input": json.RawMessage(input)}}
			// A repeated request carries the same output again; it is still one row.
			for range 2 {
				observeActivityRequest(t, proxy, thread, map[bool]string{false: "/root", true: "/root/worker"}[thread != "main"], request)
				applyObservedActivity(&u.attached, u.shell, &u.Client, &u.streams, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, &u.dirty, u.restoring, u.audience)
			}
			if len(view.Entries) != 1 || view.Entries[0].Source.Kind != "error" ||
				view.Entries[0].Source.Text != "exec script failed: SyntaxError: Unexpected token '<<'" {
				t.Fatalf("failed cell entries: %+v", view.Entries)
			}
			if !child {
				got := ansi.Strip(strings.Join(view.RenderFeed(90, 40).Lines, "\n"))
				if !strings.Contains(got, "✗ Main") || !strings.Contains(got, "│ exec script failed: SyntaxError: Unexpected token '<<'") {
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
	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	activity := sessioncontrol.NewAudience(nil)
	activity.Attach("main")
	trace := newNativeTraceFixture(t)
	proxy := &observationFixture{activity: activity, nativeTrace: nativetrace.New(trace.root)}
	setTestAppServerIntegration(u, proxy)
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
				trace.tool("main", cell.call+"-runtime", command.id, "exec_command", string(responses.MustMarshalJSON(map[string]any{"cmd": command.cmd})))
				trace.result("main", command.id, "completed", map[string]any{"exit_code": 0})
			}
		}
		trace.end("main", cell.call+"-runtime")
		input = append(input, string(responses.MustMarshalJSON(map[string]any{"type": "custom_tool_call", "call_id": cell.call, "name": "exec", "input": cell.source})))
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
	request := &codexresponses.Request{Fields: map[string]json.RawMessage{"input": json.RawMessage("[" + strings.Join(input, ",") + "]")}}
	for range 2 { // A repeated request notes each command once.
		observeActivityRequest(t, proxy, "main", "/root", request)
		applyObservedActivity(&u.attached, u.shell, &u.Client, &u.streams, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, &u.dirty, u.restoring, u.audience)
	}
	got := ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(90, 60).Lines, "\n"))
	under := "Ran git log\n      ┆ commit abc\n      " + "output not returned to the model" + "\n"
	if strings.Count(got, "output not returned to the model") != 1 || !strings.Contains(got, under) {
		t.Fatalf("want one note, under git log's output:\n%s", got)
	}
}
