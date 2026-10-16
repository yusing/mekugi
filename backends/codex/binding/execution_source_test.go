package binding

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSingleExecutionCallPreservesLiteralArguments(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         map[string]json.RawMessage
	}{
		{
			"object", `text(await tools.exec_command({cmd:"printf 'a\\b'\n",extension:{enabled:false,items:[null,"雪",2]},yield_time_ms:0}));`,
			map[string]json.RawMessage{
				"name":      json.RawMessage(`"exec_command"`),
				"arguments": json.RawMessage(`"{\"cmd\":\"printf 'a\\\\b'\\n\",\"extension\":{\"enabled\":false,\"items\":[null,\"雪\",2]},\"yield_time_ms\":0}"`),
			},
		},
		{
			"freeform", `text(await tools.apply_patch("*** Begin Patch\n*** Add File: a b.txt\n+雪\\path\n*** End Patch\n"));`,
			map[string]json.RawMessage{
				"name":  json.RawMessage(`"apply_patch"`),
				"input": json.RawMessage(`"*** Begin Patch\n*** Add File: a b.txt\n+雪\\path\n*** End Patch\n"`),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, metadata := range []bool{false, true} {
				got, ok := SingleExecutionCall(tc.source, metadata)
				if !ok || !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("metadata=%v: got %s, %v; want %s", metadata, got, ok, tc.want)
				}
			}
		})
	}
}

func TestExecutionCallsResultMetadataAndBatch(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		metadata     bool
		count        int
	}{
		{"complete result", `const r = await tools.write_stdin({session_id:42}); text(r);`, true, 1},
		{"output preview", `const r = await tools.write_stdin({session_id:42}); text(r.output);`, false, 1},
		{"output lacks metadata", `const r = await tools.write_stdin({session_id:42}); text(r.output);`, true, 0},
		{"bare preview", `await tools.write_stdin({session_id:42});`, false, 1},
		{"bare lacks metadata", `await tools.write_stdin({session_id:42});`, true, 0},
		{"batch preview", `const r = await Promise.allSettled([tools.exec_command({cmd:"printf first"}),tools.write_stdin({session_id:42})]); text(r);`, false, 2},
		{"batch lacks correlation", `const r = await Promise.allSettled([tools.exec_command({cmd:"printf first"}),tools.write_stdin({session_id:42})]); text(r);`, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, ok := ExecutionCalls(tc.source, tc.metadata)
			if ok != (tc.count > 0) || len(calls) != tc.count {
				t.Fatalf("calls=%s, ok=%v; want %d calls", calls, ok, tc.count)
			}
			if tc.count == 2 {
				want := []map[string]json.RawMessage{
					{"name": json.RawMessage(`"exec_command"`), "arguments": json.RawMessage(`"{\"cmd\":\"printf first\"}"`)},
					{"name": json.RawMessage(`"write_stdin"`), "arguments": json.RawMessage(`"{\"session_id\":42}"`)},
				}
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("batch=%s, want ordered original calls %s", calls, want)
				}
				if call, single := SingleExecutionCall(tc.source, tc.metadata); single || call != nil {
					t.Fatalf("batch recognized as single call: %s", call)
				}
			}
		})
	}
}

func TestExecutionCallsRejectsOpaqueSource(t *testing.T) {
	for _, source := range []string{
		`text(await tools.exec_command({cmd:command}));`,
		`text(await tools[toolName]({cmd:"pwd"}));`,
		`if (enabled) text(await tools.exec_command({cmd:"pwd"}));`,
		`const tools = other; text(await tools.exec_command({cmd:"pwd"}));`,
		`text(await tools.exec_command({cmd:"pwd"})); const tools = other; text(tools);`,
		`text(await tools.exec_command({cmd:"pwd"})); sideEffect();`,
		`const r = await Promise.allSettled([tools.exec_command({cmd:"pwd"})]); r.forEach(r => { sideEffect(); text(r); });`,
	} {
		t.Run(source, func(t *testing.T) {
			for _, metadata := range []bool{false, true} {
				if calls, ok := ExecutionCalls(source, metadata); ok || len(calls) != 0 {
					t.Fatalf("metadata=%v: opaque source recognized as %s", metadata, calls)
				}
			}
		})
	}
}

func TestExecutionCallsQualifiedToolNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"mcp__server__custom_tool", true},
		{"server__custom_tool", true},
		{"clock__curr_time", true},
		{"unknown_tool", false},
		{"mcp__server__", false},
		{"mcp____custom_tool", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call, ok := SingleExecutionCall(`text(await tools.`+tc.name+`({}));`, true)
			if ok != tc.ok {
				t.Fatalf("qualified name recognized=%v, want %v", ok, tc.ok)
			}
			if ok && string(call["name"]) != `"`+tc.name+`"` {
				t.Fatalf("tool name changed: %s", call)
			}
		})
	}
}
