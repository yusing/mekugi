package router

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestSubagentShellExcerptsJSONAndSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			root, _ := prepareActivityTest(t, proxy, "root", "r", "", "/root", nil)
			command := "go test ./internal/router\nprintf done"
			input := []any{
				map[string]any{"type": "function_call", "call_id": "run", "name": "exec_command", "arguments": string(mustMarshalJSON(map[string]any{"cmd": command}))},
				map[string]any{"type": "function_call_output", "call_id": "run", "output": "Chunk ID: abc\nWall time: 1 seconds\nProcess running with session ID 26369\nFinal output:\n"},
			}
			child, _ := prepareActivityTest(t, proxy, "child", "c", "r", "/root/worker", input)
			calls := []map[string]any{
				{"type": "custom_tool_call", "id": "poll", "call_id": "poll", "name": "exec", "input": `text(await tools.write_stdin({session_id:26369,chars:""}));`},
				{"type": "custom_tool_call", "id": "batch-poll", "call_id": "batch-poll", "name": "exec", "input": `text(await tools.write_stdin({session_id:26369,chars:""})); text(await tools.clock__curr_time({}));`},
			}
			payload := mustMarshalJSON(map[string]any{"status": "completed", "output": calls})
			if stream {
				for _, call := range calls {
					if _, err := child.TransformSSE(mustMarshalJSON(map[string]any{"type": "response.output_item.done", "item": call})); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := child.TransformSSE(mustMarshalJSON(map[string]any{"type": "response.completed", "response": json.RawMessage(payload)})); err != nil {
					t.Fatal(err)
				}
			} else if _, err := child.TransformJSON(payload); err != nil {
				t.Fatal(err)
			}
			var output []byte
			if stream {
				events, err := root.TransformSSE([]byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`))
				if err != nil {
					t.Fatal(err)
				}
				output = bytes.Join(events, nil)
			} else {
				var err error
				output, err = root.TransformJSON([]byte(`{"status":"completed","output":[]}`))
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, want := range []string{"Still Running", "go test ./internal/router…"} {
				if !bytes.Contains(output, []byte(want)) {
					t.Fatalf("missing %q: %s", want, output)
				}
			}
			for _, hidden := range []string{"26369", "@shell/", "printf done", "command unavailable"} {
				if bytes.Contains(output, []byte(hidden)) {
					t.Fatalf("leaked %q: %s", hidden, output)
				}
			}
		})
	}
}

func TestShellActivitySessionCorrelation(t *testing.T) {
	transform := &mekugiResponseTransform{}
	for _, output := range []any{
		map[string]any{"session_id": 42, "output": "start"},
		`{"session_id":42,"output":"start"}`,
		[]any{map[string]any{"type": "text", "text": `{"session_id":42,"output":"start"}`}},
	} {
		transform.prepareShellActivity(mustMarshalJSON([]any{
			map[string]any{"type": "custom_tool_call", "call_id": "a", "name": "exec", "input": `text(await tools.exec_command({cmd:"sleep 20"}));`},
			map[string]any{"type": "function_call", "call_id": "b", "name": "exec_command", "arguments": `{"cmd":"sleep 30"}`},
			map[string]any{"type": "custom_tool_call_output", "call_id": "a", "output": output},
		}))
		if got := transform.activityShellSessions["42"]; got != "sleep 20" {
			t.Fatalf("wrong command: %q", got)
		}
	}
	transform.prepareShellActivity([]byte(`[]`))
	if len(transform.activityShellSessions) != 0 {
		t.Fatal("request inherited unrelated session")
	}
	if got := toolActivityOutputSession(mustMarshalJSON("Output:\nProcess running with session ID 42")); got != "" {
		t.Fatal("parsed program output as metadata")
	}
}

func TestShellActivityExitCodeUsesHostMetadata(t *testing.T) {
	for _, tc := range []struct {
		output string
		mode   bool
		want   int
		ok     bool
	}{
		{`{"exit_code":1,"output":""}`, false, 1, true},
		{"Chunk ID: a\nProcess exited with code 2\nOutput:\nProcess exited with code 3", false, 2, true},
		{"Script completed\nWall time 0.1 seconds\nOutput:\n{\"exit_code\":4,\"output\":\"\"}", true, 4, true},
		{"Script completed\nWall time 0.1 seconds\nOutput:\n", true, 0, false},
		{"Chunk ID: a\nOutput:\nProcess exited with code 3", false, 0, false},
		{"Script completed\nOutput:\nanything {\"exit_code\":5}", true, 0, false},
	} {
		got, ok := toolActivityExitCode(mustMarshalJSON(tc.output), tc.mode)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%q: exit = %d, %t", tc.output, got, ok)
		}
	}
}

func TestShellActivityExitEventCorrelatesCompletedCall(t *testing.T) {
	activity := newSubagentActivity()
	activity.observe("root", "", "/root", false)
	activity.observe("child", "root", "/root/a", true)
	transform := &mekugiResponseTransform{proxy: &mekugiProxy{activity: activity}, threadID: "child"}
	input := mustMarshalJSON([]any{
		map[string]any{"type": "function_call", "id": "call-item", "call_id": "call", "name": "exec_command", "arguments": `{"cmd":"false"}`},
		map[string]any{"type": "function_call_output", "call_id": "call", "output": "Chunk ID: a\nProcess exited with code 1\nOutput:\n"},
	})
	transform.collectShellExits(input)
	if len(activity.events) != 1 || activity.events[0].kind != "exit" || activity.events[0].callID != "call-item" || activity.events[0].raw != "1" {
		t.Fatalf("exit activity = %+v", activity.events)
	}
	transform.collectShellExits(input)
	if len(activity.events) != 1 {
		t.Fatalf("duplicate exit activity = %+v", activity.events)
	}
	// A JavaScript printout is not a host command result, even when its JSON
	// resembles the result object. A transparent one-call projection is.
	transform.collectShellExits(mustMarshalJSON([]any{
		map[string]any{"type": "custom_tool_call", "id": "fake-item", "call_id": "fake", "name": "exec", "input": `text({exit_code:7});`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "fake", "output": "Script completed\nWall time 0.1 seconds\nOutput:\n{\"exit_code\":7}"},
	}))
	if len(activity.events) != 1 {
		t.Fatalf("fabricated status = %+v", activity.events)
	}
	transform.collectShellExits(mustMarshalJSON([]any{
		map[string]any{"type": "custom_tool_call", "id": "real-item", "call_id": "real", "name": "exec", "input": `text(await tools.exec_command({cmd:"false"}));`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "real", "output": []any{
			map[string]any{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"},
			map[string]any{"type": "input_text", "text": `{"exit_code":3,"output":""}`},
		}},
	}))
	if len(activity.events) != 2 || activity.events[1].callID != "real-item" || activity.events[1].raw != "3" {
		t.Fatalf("split Code Mode status = %+v", activity.events)
	}
	transform.collectShellExits(mustMarshalJSON([]any{
		map[string]any{"type": "function_call", "id": "yield-item", "call_id": "yield", "name": "exec_command", "arguments": `{"cmd":"false"}`},
		map[string]any{"type": "function_call_output", "call_id": "yield", "output": `{"session_id":19,"output":""}`},
		map[string]any{"type": "function_call", "id": "poll-item", "call_id": "poll", "name": "write_stdin", "arguments": `{"session_id":19,"chars":""}`},
		map[string]any{"type": "function_call_output", "call_id": "poll", "output": `{"exit_code":5,"output":""}`},
	}))
	if len(activity.events) != 3 || activity.events[2].callID != "yield-item" || activity.events[2].raw != "5" {
		t.Fatalf("yielded command status = %+v", activity.events)
	}
	transform.collectShellExits(mustMarshalJSON([]any{
		map[string]any{"type": "custom_tool_call", "id": "cell-item", "call_id": "cell", "name": "exec", "input": `text(await tools.exec_command({cmd:"false"}));`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "cell", "output": "Script running with cell ID c-1\nWall time 0.1 seconds\nOutput:\n"},
		map[string]any{"type": "custom_tool_call", "id": "wait-item", "call_id": "wait", "name": "wait", "input": `{"cell_id":"c-1"}`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "wait", "output": "Script completed\nWall time 0.2 seconds\nOutput:\n{\"exit_code\":6,\"output\":\"\"}"},
	}))
	if len(activity.events) != 4 || activity.events[3].callID != "cell-item" || activity.events[3].raw != "6" {
		t.Fatalf("yielded Code Mode status = %+v", activity.events)
	}
	transform.collectShellExits(mustMarshalJSON([]any{
		map[string]any{"type": "custom_tool_call", "id": "session-item", "call_id": "session", "name": "exec", "input": `text(await tools.exec_command({cmd:"false",yield_time_ms:1000}));`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "session", "output": "Script completed\nWall time 0.1 seconds\nOutput:\n{\"session_id\":44,\"output\":\"\"}"},
		map[string]any{"type": "function_call", "id": "session-poll", "call_id": "session-poll", "name": "write_stdin", "arguments": `{"session_id":44,"chars":""}`},
		map[string]any{"type": "function_call_output", "call_id": "session-poll", "output": `{"exit_code":7,"output":""}`},
	}))
	if len(activity.events) != 5 || activity.events[4].callID != "session-item" || activity.events[4].raw != "7" {
		t.Fatalf("Code Mode session status = %+v", activity.events)
	}
	transform.collectShellExits(mustMarshalJSON([]any{
		map[string]any{"type": "custom_tool_call", "id": "handoff-item", "call_id": "handoff", "name": "exec", "input": `text(await tools.exec_command({cmd:"false",yield_time_ms:1000}));`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "handoff", "output": "Script running with cell ID c-2\nWall time 0.1 seconds\nOutput:\n"},
		map[string]any{"type": "custom_tool_call", "id": "handoff-wait", "call_id": "handoff-wait", "name": "wait", "input": `{"cell_id":"c-2"}`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "handoff-wait", "output": "Script completed\nWall time 0.2 seconds\nOutput:\n{\"session_id\":45,\"output\":\"\"}"},
		map[string]any{"type": "function_call", "id": "handoff-poll", "call_id": "handoff-poll", "name": "write_stdin", "arguments": `{"session_id":45,"chars":""}`},
		map[string]any{"type": "function_call_output", "call_id": "handoff-poll", "output": `{"exit_code":8,"output":""}`},
	}))
	if len(activity.events) != 6 || activity.events[5].callID != "handoff-item" || activity.events[5].raw != "8" {
		t.Fatalf("cell-to-session status = %+v", activity.events)
	}
	if got := drainText(activity.drain("root", time.Now(), maxCommentaryPublicationBytes)); got != "" {
		t.Fatalf("pane-only exit leaked into root commentary: %q", got)
	}
}

func TestShellActivityCodeModeSessionCorrelation(t *testing.T) {
	const sessionID = 35274
	const header = "Script completed\nWall time 0.2 seconds\nOutput:\n"
	const payload = `{"output":"partial","session_id":35274}`
	for _, origin := range []string{
		`text(await tools.exec_command({cmd:"go test ./internal/router",yield_time_ms:1000}));`,
		`const r = await tools.exec_command({cmd:"go test ./internal/router",yield_time_ms:1000}); text(r);`,
	} {
		for _, output := range []any{header + payload, []any{map[string]any{"type": "input_text", "text": header}, map[string]any{"type": "input_text", "text": payload}}} {
			tr := &mekugiResponseTransform{}
			tr.prepareShellActivity(mustMarshalJSON([]any{
				map[string]any{"type": "custom_tool_call", "call_id": "run", "name": "exec", "input": origin},
				map[string]any{"type": "custom_tool_call_output", "call_id": "run", "output": output},
			}))
			if got := tr.activityShellSessions["35274"]; got != "go test ./internal/router" {
				t.Fatalf("origin %q: missing command for session %d: %q", origin, sessionID, got)
			}
			poll := map[string]json.RawMessage{"input": mustMarshalJSON(`text(await tools.write_stdin({session_id:35274,chars:"",yield_time_ms:300000}));`)}
			if got, ok := tr.shellActivityDisplay(poll, "exec"); !ok || got != "Still Running\n`go test ./internal/router`" {
				t.Fatalf("origin %q: poll = %q, %v", origin, got, ok)
			}
		}
	}
	tr := &mekugiResponseTransform{}
	tr.prepareShellActivity(mustMarshalJSON([]any{
		map[string]any{"type": "custom_tool_call", "call_id": "run", "name": "exec", "input": `text((await tools.exec_command({cmd:"go test ./internal/router"})).output);`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "run", "output": header + payload},
	}))
	if len(tr.activityShellSessions) != 0 {
		t.Fatalf("output-only projection claimed session metadata: %+v", tr.activityShellSessions)
	}
	tr.prepareShellActivity(mustMarshalJSON([]any{
		map[string]any{"type": "custom_tool_call", "call_id": "run", "name": "exec", "input": `text(await tools.exec_command({cmd:"go test ./internal/router"}));`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "run", "output": "Script failed\nWall time 0.2 seconds\nOutput:\n" + `{"output":"partial","session_id":35274}`},
	}))
	if len(tr.activityShellSessions) != 0 {
		t.Fatalf("failed Code Mode script claimed session metadata: %+v", tr.activityShellSessions)
	}
}

func TestShellActivityCodeModeYieldedSessionCorrelation(t *testing.T) {
	const running = "Script running with cell ID 7\nWall time 30 seconds\nOutput:\n"
	const completed = "Script completed\nWall time 0.2 seconds\nOutput:\n" + `{"output":"partial","session_id":35274}`
	tr := &mekugiResponseTransform{}
	tr.prepareShellActivity(mustMarshalJSON([]any{
		map[string]any{"type": "custom_tool_call", "call_id": "run", "name": "exec", "input": `text(await tools.exec_command({cmd:"go test ./internal/router",yield_time_ms:1000}));`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "run", "output": running},
		map[string]any{"type": "function_call", "call_id": "wait", "name": "wait", "arguments": `{"cell_id":"7"}`},
		map[string]any{"type": "function_call_output", "call_id": "wait", "output": completed},
	}))
	if got := tr.activityShellSessions["35274"]; got != "go test ./internal/router" {
		t.Fatalf("yielded Code Mode command lost session origin: %q", got)
	}
	if len(tr.activityCellOperations) != 0 {
		t.Fatalf("completed cell still active: %+v", tr.activityCellOperations)
	}
	poll := map[string]json.RawMessage{"input": mustMarshalJSON(`text(await tools.write_stdin({session_id:35274,chars:""}));`)}
	if got, ok := tr.shellActivityDisplay(poll, "exec"); !ok || got != "Still Running\n`go test ./internal/router`" {
		t.Fatalf("yielded session poll = %q, %v", got, ok)
	}
	tr.prepareShellActivity(mustMarshalJSON([]any{
		map[string]any{"type": "custom_tool_call", "call_id": "run", "name": "exec", "input": `text((await tools.exec_command({cmd:"go test ./internal/router"})).output);`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "run", "output": running},
		map[string]any{"type": "function_call", "call_id": "wait", "name": "wait", "arguments": `{"cell_id":"7"}`},
		map[string]any{"type": "function_call_output", "call_id": "wait", "output": completed},
	}))
	if len(tr.activityShellSessions) != 0 {
		t.Fatalf("output-only projection claimed yielded session metadata: %+v", tr.activityShellSessions)
	}
}

func TestShellActivityDoesNotCorrelateProgramOutput(t *testing.T) {
	for _, misleading := range []string{`{"session_id":42}`, "Process running with session ID 42"} {
		transform := &mekugiResponseTransform{}
		transform.prepareShellActivity(mustMarshalJSON([]any{
			map[string]any{"type": "function_call", "call_id": "real", "name": "exec_command", "arguments": `{"cmd":"sleep 20"}`},
			map[string]any{"type": "function_call_output", "call_id": "real", "output": `{"session_id":42}`},
			map[string]any{"type": "custom_tool_call", "call_id": "stdout", "name": "exec", "input": `const r = await tools.exec_command({cmd:"printf misleading"}); text(r.output);`},
			map[string]any{"type": "custom_tool_call_output", "call_id": "stdout", "output": misleading},
		}))
		if got := transform.activityShellSessions["42"]; got != "sleep 20" {
			t.Fatalf("program output replaced real session: %q", got)
		}
	}
}

func TestShellBatchActivityExcerpt(t *testing.T) {
	const source = "#!params={}\nprintf one\n#!python3\nprint(2)"
	if got := toolActivityCommandExcerpt(source); got != "printf one…" {
		t.Fatalf("batch excerpt exposes framing instead of source: %q", got)
	}
}

func TestCellActivityCorrelation(t *testing.T) {
	origin := map[string]any{"type": "custom_tool_call", "call_id": "origin", "name": "exec", "input": `text(await tools.exec_command({cmd:"go test ./internal/router"}));`}
	wait := map[string]any{"type": "function_call", "call_id": "poll", "name": "wait", "arguments": `{"cell_id":"7"}`}
	header := "Script running with cell ID 7\nWall time 30 seconds\nOutput:\n"
	for _, output := range []any{header, []any{map[string]any{"type": "input_text", "text": header}}} {
		t.Run("yield", func(t *testing.T) {
			tr := &mekugiResponseTransform{}
			input := []any{origin, map[string]any{"type": "custom_tool_call_output", "call_id": "origin", "output": output}, wait,
				map[string]any{"type": "function_call_output", "call_id": "poll", "output": output}}
			tr.prepareShellActivity(mustMarshalJSON(input))
			if got := tr.activityCellOperations["7"]; got != "```bash\ngo test ./internal/router\n```" {
				t.Fatalf("operation: %q", got)
			}
			input = append(input, map[string]any{"type": "function_call", "call_id": "done", "name": "wait", "arguments": `{"cell_id":"7"}`},
				map[string]any{"type": "function_call_output", "call_id": "done", "output": "Script completed\nWall time 1 seconds\nOutput:\n"})
			tr.prepareShellActivity(mustMarshalJSON(input))
			if len(tr.activityCellOperations) != 0 {
				t.Fatal("completed cell retained")
			}
			tr.prepareShellActivity([]byte(`[]`))
			if len(tr.activityCellOperations) != 0 {
				t.Fatal("inherited another request")
			}
		})
	}
	for _, output := range []any{
		"Output:\n" + header,
		[]any{map[string]any{"type": "input_text", "text": "Script completed\nWall time 1 seconds\nOutput:\n"}, map[string]any{"type": "input_text", "text": header}},
	} {
		tr := &mekugiResponseTransform{}
		tr.prepareShellActivity(mustMarshalJSON([]any{origin, map[string]any{"type": "custom_tool_call_output", "call_id": "origin", "output": output}}))
		if len(tr.activityCellOperations) != 0 {
			t.Fatal("program output treated as cell metadata")
		}
	}
}

func TestCellActivityWrappedSessionPoll(t *testing.T) {
	tr := &mekugiResponseTransform{}
	tr.prepareShellActivity(mustMarshalJSON([]any{
		continuationTestCall("exec_command", "run", `{"cmd":"go test ./internal/router"}`),
		map[string]any{"type": "function_call_output", "call_id": "run", "output": `{"session_id":42}`},
		continuationTestCall("exec", "poll", `text(await tools.write_stdin({session_id:42,chars:""}));`),
		continuationTestOutput("poll", "Script running with cell ID 7\nWall time 30 seconds\nOutput:\n"),
	}))
	if got := tr.activityCellOperations["7"]; got != "`go test ./internal/router`" {
		t.Fatalf("poll origin: %q", got)
	}
}

func TestUnknownSessionPollDoesNotBecomeCellOrigin(t *testing.T) {
	tr := &mekugiResponseTransform{}
	tr.prepareShellActivity(mustMarshalJSON([]any{
		continuationTestCall("exec", "poll", `text(await tools.write_stdin({session_id:42,chars:""}));`),
		continuationTestOutput("poll", "Script running with cell ID 7\nWall time 30 seconds\nOutput:\n"),
	}))
	if operation := tr.activityCellOperations["7"]; operation != "" {
		t.Fatalf("unknown command became a cell origin: %q", operation)
	}
	if got, ok := tr.shellActivityDisplay(map[string]json.RawMessage{"arguments": mustMarshalJSON(`{"session_id":42,"chars":""}`)}, "write_stdin"); !ok || got != "Still Running" {
		t.Fatalf("unknown session poll = %q, %v", got, ok)
	}
}

func TestActivityStreamsOutputDeltaEstimate(t *testing.T) {
	activity := newSubagentActivity()
	activity.observe("root", "", "/root", false)
	activity.observe("child", "root", "/root/a", true)
	transform := &mekugiResponseTransform{proxy: &mekugiProxy{activity: activity}, threadID: "child"}
	for _, event := range []map[string]any{
		{"type": "response.output_text.delta", "item_id": "m", "delta": "hello world!"},
		{"type": "response.function_call_arguments.delta", "item_id": "f", "delta": "{}"},
		{"type": "response.output_text.done", "item_id": "m", "delta": "ignored"},
	} {
		if _, err := transform.transformActivitySSE(mustMarshalJSON(event)); err != nil {
			t.Fatal(err)
		}
	}
	if got := activity.threads["child"].streamed; got != 14 {
		t.Fatalf("streamed bytes = %d, want 14", got)
	}
}
