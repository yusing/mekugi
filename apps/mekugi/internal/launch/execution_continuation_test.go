package launch

import (
	"encoding/json"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func continuationTestCall(name, id, input string) map[string]json.RawMessage {
	field, kind := "input", "custom_tool_call"
	if name == "wait" || name == "write_stdin" || name == "exec_command" {
		field, kind = "arguments", "function_call"
	}
	return map[string]json.RawMessage{
		"type": responses.MustMarshalJSON(kind), "name": responses.MustMarshalJSON(name),
		"call_id": responses.MustMarshalJSON(id), field: responses.MustMarshalJSON(input),
	}
}

func continuationTestOutput(id string, parts ...string) map[string]json.RawMessage {
	var content []any
	for _, part := range parts {
		content = append(content, map[string]any{"type": "input_text", "text": part})
	}
	return map[string]json.RawMessage{
		"type": responses.MustMarshalJSON("custom_tool_call_output"), "call_id": responses.MustMarshalJSON(id),
		"output": responses.MustMarshalJSON(content),
	}
}

func TestNativeExecutionMetadataBoundary(t *testing.T) {
	for _, test := range []struct {
		text string
		id   int64
	}{
		{"Chunk ID: abc\nWall time: 0.1000 seconds\nProcess running with session ID 42\nOutput:\nhello", 42},
		{"Wall time: 0.1000 seconds\nProcess running with session ID 43\nOriginal token count: 500\nOutput:\nhello", 43},
		{"Chunk ID: abc\nWall time: 0.1000 seconds\nProcess running with session ID 42\nFinal output:\nhello", 42},
		{"Wall time: 0.1000 seconds\nProcess running with session ID 43\nOriginal token count: 500\nFinal output:\nhello", 43},
		{"Wall time: 0.1000 seconds\nProcess exited with code 0\nFinal output:\nProcess running with session ID 42\n", 0},
		{"Wall time: 0.1000 seconds\nProcess exited with code 0\nOutput:\nProcess running with session ID 42\n", 0},
		{"Process running with session ID 42\nOutput:\n", 0},
		{"Wall time: 0.1000 seconds\nProcess running with session ID -1\nOutput:\n", 0},
	} {
		if got := binding.NativeExecutionSession(test.text); got != test.id {
			t.Errorf("binding.NativeExecutionSession(%q)=%d, want %d", test.text, got, test.id)
		}
	}
}

func TestExecutionContinuationRecognizesSessionJavaScriptFromHistory(t *testing.T) {
	// Sanitized exec inputs observed in September 2026 sessions. Session
	// IDs are normalized; the old pragma remains readable in history but must
	// not be emitted in a new continuation.
	for _, test := range []struct {
		name   string
		source string
	}{
		{"plain timed wait", `text(await tools.write_stdin({session_id: 42,chars:"",yield_time_ms:1000,max_output_tokens:1200}));`},
		{"plain default wait", `text(await tools.write_stdin({chars:"",session_id: 42}));`},
		{"historical pragma", "// @exec: {\"yield_time_ms\": 300000}\n" +
			`text(await tools.write_stdin({session_id: 42,chars:"",yield_time_ms:300000,max_output_tokens:2500}));`},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := continuationTestCall("exec", "resume", test.source)
			if got := binding.ExecutionResumeHandle(call, "exec"); got != "session:42" {
				t.Fatalf("resume handle = %q, want session:42", got)
			}
		})
	}
}
