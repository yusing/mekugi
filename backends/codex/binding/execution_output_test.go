package binding

import (
	jsonv1 "encoding/json"
	"reflect"
	"testing"
)

func TestDecodeExecutionResult(t *testing.T) {
	native := func(state string) string { return "Chunk ID: 1\nWall time: 0.1 seconds\n" + state + "\nOutput:\n" }
	cell := func(state string) string { return state + "\nWall time 0.1 seconds\nOutput:\n" }
	for _, tc := range []struct {
		name, tool, text string
		want             ExecutionResult
	}{
		{"exit zero", "exec_command", native("Process exited with code 0"), ExecutionResult{Terminal: true, Succeeded: true, ExitCode: new(0)}},
		{"failed", "exec_command", native("Process exited with code 2"), ExecutionResult{Terminal: true, ExitCode: new(2)}},
		{"out of range exit", "exec_command", native("Process exited with code 300"), ExecutionResult{Terminal: true, ExitCode: new(300)}},
		{"running", "exec_command", native("Process running with session ID 7"), ExecutionResult{ResumeHandle: "session:7"}},
		{"aborted", "exec_command", "aborted by user", ExecutionResult{Terminal: true}},
		{"closed stdin", "write_stdin", "write_stdin failed: stdin is closed for this session", ExecutionResult{}},
		{"vanished session", "write_stdin", "Unknown process id 7", ExecutionResult{Terminal: true}},
		{"terminal poll", "write_stdin", native("Process exited with code 0"), ExecutionResult{Terminal: true, Succeeded: true, ExitCode: new(0)}},
		{"cell completed", "exec", cell("Script completed"), ExecutionResult{Terminal: true, Succeeded: true}},
		{"cell failed", "exec", cell("Script failed"), ExecutionResult{Terminal: true}},
		{"cell terminated", "exec", cell("Script terminated"), ExecutionResult{Terminal: true}},
		{"cell running", "exec", cell("Script running with cell ID cell-7"), ExecutionResult{ResumeHandle: "cell:cell-7"}},
		{"printed nested header", "exec", cell("Script completed") + native("Process running with session ID 7"), ExecutionResult{Terminal: true, Succeeded: true}},
		{"unframed cell output", "exec", "Script completed", ExecutionResult{}},
		{"patch succeeded", "apply_patch", "  Success. Updated file", ExecutionResult{Terminal: true, Succeeded: true}},
		{"patch rejected", "apply_patch", "Failed to find expected lines", ExecutionResult{Terminal: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.want.Text = tc.text
			for _, output := range []any{tc.text, []map[string]string{{"type": "input_text", "text": tc.text}}, map[string]any{"content": tc.text}} {
				if got := DecodeExecutionResult(tc.tool, executionEvidenceJSON(t, output)); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("decoded result = %+v, want %+v", got, tc.want)
				}
			}
		})
	}
}

func TestDecodeExecutionResultRetainedPatchSuccess(t *testing.T) {
	// Retained wrapper fields keep v1 matching and last-value semantics. The
	// explicit host success field overrides success-like output text.
	raw := jsonv1.RawMessage(`{"CONTENT":"Success. printed","SUCCESS":true,"success":false}`)
	if got := DecodeExecutionResult("apply_patch", raw); !got.Terminal || got.Succeeded || got.Text != "Success. printed" {
		t.Fatalf("retained patch result = %+v", got)
	}
}
