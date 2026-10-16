package binding

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"reflect"
	"testing"
)

func executionEvidenceJSON(t *testing.T, value any) jsonv1.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func executionEvidenceItem(t *testing.T, fields map[string]string) map[string]jsonv1.RawMessage {
	t.Helper()
	item := make(map[string]jsonv1.RawMessage, len(fields))
	for key, value := range fields {
		item[key] = executionEvidenceJSON(t, value)
	}
	return item
}

func TestExecutionEvidenceClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fields   map[string]string
		retained *RetainedExecution
		want     ExecutionCall
	}{
		{"original native", map[string]string{"name": "functions.exec_command"}, nil, ExecutionCall{Native: true}},
		{"original custom", map[string]string{"name": "functions.exec", "input": `text(await tools.exec_command({cmd:"pwd"}));`}, nil, ExecutionCall{CodeMode: true, NativePayload: true}},
		{"missing retained opaque", map[string]string{"name": "exec", "input": `text("Process running with session ID 42");`}, nil, ExecutionCall{CodeMode: true}},
		{"unknown retained fallback", map[string]string{"name": "exec_command"}, &RetainedExecution{CarrierKind: "unknown"}, ExecutionCall{Native: true}},
		{"normalized retained native", map[string]string{"name": "unrecognized"}, &RetainedExecution{CarrierKind: "function", ToolName: "exec_command"}, ExecutionCall{Native: true, NativePayload: true}},
		{"retained overrides original", map[string]string{"name": "exec_command"}, &RetainedExecution{CarrierKind: "custom", ToolName: "exec", Script: `text(await tools.write_stdin({session_id:42}));`}, ExecutionCall{CodeMode: true, NativePayload: true}},
		{"retained output lacks metadata", map[string]string{"name": "exec_command"}, &RetainedExecution{CarrierKind: "custom", ToolName: "exec", Script: `text((await tools.exec_command({cmd:"pwd"})).output);`}, ExecutionCall{CodeMode: true}},
		{"foreign namespace", map[string]string{"namespace": "other", "name": "exec_command"}, &RetainedExecution{CarrierKind: "function", ToolName: "exec_command"}, ExecutionCall{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyExecutionCall(executionEvidenceItem(t, tc.fields), tc.retained, "functions.exec")
			if got != tc.want {
				t.Fatalf("classification = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestExecutionEvidenceResumeAndMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, tool, args, source, handle string
		want                             ExecutionCall
	}{
		{"wait", "functions.wait", `{"cell_id":"abc"}`, "", "cell:abc", ExecutionCall{CodeMode: true, CellID: "abc", ResumeHandle: "cell:abc"}},
		{"native resume", "write_stdin", `{"session_id":42}`, "", "session:42", ExecutionCall{Native: true, ResumeHandle: "session:42"}},
		{"output projection retains resume only", "exec", "", `const r = await tools.write_stdin({session_id:42}); text(r.output);`, "session:42", ExecutionCall{CodeMode: true, ResumeHandle: "session:42"}},
		{"full result retains metadata", "exec", "", `const r = await tools.write_stdin({session_id:42}); text(r);`, "session:42", ExecutionCall{CodeMode: true, NativePayload: true, ResumeHandle: "session:42"}},
		{"printed handle is opaque", "exec", "", `text("session:42");`, "", ExecutionCall{CodeMode: true}},
		{"invalid session", "write_stdin", `{"session_id":0}`, "", "", ExecutionCall{Native: true}},
		{"malformed arguments", "wait", `{`, "", "", ExecutionCall{}},
		{"v1 case insensitive and duplicate fields", "wait", `{"CELL_ID":"old","cell_id":"abc"}`, "", "cell:abc", ExecutionCall{CodeMode: true, CellID: "abc", ResumeHandle: "cell:abc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := executionEvidenceItem(t, map[string]string{"name": tc.tool, "arguments": tc.args, "input": tc.source})
			if got := ExecutionResumeHandle(item, "functions.exec"); got != tc.handle {
				t.Fatalf("resume = %q, want %q", got, tc.handle)
			}
			if got := ClassifyExecutionCall(item, nil, "functions.exec"); got != tc.want {
				t.Fatalf("classification = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestExecutionEvidenceOutputPositions(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      []string
	}{
		{"string", `"first\nsecond"`, []string{"first\nsecond"}},
		{"empty string", `""`, []string{""}},
		{"content positions", `[{"type":"input_text","text":"first"},{"type":"image","text":"ignored"},{"type":"input_text","text":""},{"type":"input_text","text":"last"}]`, []string{"first", "", "", "last"}},
		{"first part must be text", `[{"type":"image"},{"type":"input_text","text":"later"}]`, nil},
		{"empty parts", `[]`, nil},
		{"object", `{"type":"input_text","text":"not an array"}`, nil},
		{"malformed", `[`, nil},
		{"v1 field matching", `[{"TYPE":"input_text","TEXT":"kept"}]`, []string{"kept"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExecutionOutputTexts(jsonv1.RawMessage(tc.raw)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("texts = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestExecutionEvidenceAnnotationEnvelope(t *testing.T) {
	handle := map[string]any{"kind": "native", "session_id": 42}
	annotation := ExecutionContinuation{
		Handle:   handle,
		NextCall: &ExecutionNextCall{Tool: "functions.write_stdin", Input: map[string]any{"session_id": 42}},
		Reason:   "running",
	}
	envelope := executionEvidenceJSON(t, map[string]any{"continuation": annotation})
	part := func(text string, extra bool) jsonv1.RawMessage {
		fields := map[string]any{"type": "input_text", "text": text}
		if extra {
			fields["extra"] = true
		}
		return executionEvidenceJSON(t, fields)
	}
	for _, tc := range []struct {
		name string
		part jsonv1.RawMessage
		want bool
	}{
		{"shared wire", part(string(envelope), false), true},
		{"whitespace", part(" \n"+string(envelope)+"\n", false), true},
		{"extra content field", part(string(envelope), true), false},
		{"extra envelope field", part(`{"continuation":`+string(executionEvidenceJSON(t, annotation))+`,"extra":true}`, false), false},
		{"extra annotation field", part(`{"continuation":{"handle":{"kind":"native","session_id":42},"next_call":null,"extra":true}}`, false), false},
		{"extra next call field", part(`{"continuation":{"handle":{"kind":"native","session_id":42},"next_call":{"tool":"wait","input":{},"extra":true}}}`, false), false},
		{"different handle", part(`{"continuation":{"handle":{"kind":"native","session_id":43},"next_call":null}}`, false), false},
		{"extra handle field", part(`{"continuation":{"handle":{"kind":"native","session_id":42,"extra":true},"next_call":null}}`, false), false},
		{"missing next call", part(`{"continuation":{"handle":{"kind":"native","session_id":42}}}`, false), false},
		{"null continuation", part(`{"continuation":null}`, false), false},
		{"malformed envelope", part(`{"continuation":`, false), false},
		{"prefixed lookalike", part("advice: "+string(envelope), false), false},
		{"wrong content type", executionEvidenceJSON(t, map[string]any{"type": "text", "text": string(envelope)}), false},
		{"malformed content", jsonv1.RawMessage(`{`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExecutionAnnotationMatches(tc.part, handle); got != tc.want {
				t.Fatalf("matches = %v, want %v; part = %s", got, tc.want, tc.part)
			}
		})
	}
}
