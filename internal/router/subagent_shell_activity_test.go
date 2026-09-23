package router

import (
	"bytes"
	"encoding/json"
	"testing"
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
