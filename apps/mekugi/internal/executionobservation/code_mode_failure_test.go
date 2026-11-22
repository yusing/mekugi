package executionobservation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestCodeModeReturnedNothing(t *testing.T) {
	header := "Script completed\nWall time 0.1 seconds\nOutput:\n"
	for raw, want := range map[string]bool{
		string(responses.MustMarshalJSON(header)):                                                                                                   true,
		string(responses.MustMarshalJSON(strings.TrimSuffix(header, "\n"))):                                                                         true,
		string(responses.MustMarshalJSON(header + "  \n")):                                                                                          true,
		string(responses.MustMarshalJSON(header + "printed")):                                                                                       false,
		`[{"type":"input_text","text":` + string(responses.MustMarshalJSON(header)) + `},{"type":"input_text","text":""}]`:                          true,
		`[{"type":"input_text","text":` + string(responses.MustMarshalJSON(header)) + `},{"type":"input_text","text":"x"}]`:                         false,
		`[{"type":"input_text","text":` + string(responses.MustMarshalJSON(header)) + `},{"type":"input_image","image_url":""}]`:                    false,
		`[{"type":"input_text","text":"Script failed\nWall time 0.0 seconds\nOutput:\n"},{"type":"input_text","text":"Script error:\nTypeError"}]`:  true,
		`[{"type":"input_text","text":"Script completed\nWall time 0.0 seconds\nOutput:\n"},{"type":"input_text","text":"Script error:\nprinted"}]`: false,
		string(responses.MustMarshalJSON("Script running with cell ID 7\nWall time 10.0 seconds\nOutput:\n")):                                       false,
		string(responses.MustMarshalJSON("Script completed (more output: wait with cell ID 7)\nWall time 0.1 seconds\nOutput:\n")):                  false,
	} {
		if got := codeModeReturnedNothing(json.RawMessage(raw)); got != want {
			t.Errorf("codeModeReturnedNothing(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestObserveCodeModeFailuresUsesPagedWaitErrorDetail(t *testing.T) {
	audience := sessioncontrol.NewAudience(nil)
	audience.Attach("thread-1")
	audience.Observe("thread-1", "", "/root", false)
	preparer := &Preparer{Config: Config{Audience: audience}}

	execCall := map[string]any{
		"type":    "custom_tool_call",
		"call_id": "call-exec-1",
		"name":    "exec",
		"input":   "throw new Error('boom')",
	}
	page1Output := map[string]any{
		"type":    "custom_tool_call_output",
		"call_id": "call-exec-1",
		"output": []map[string]string{
			{"type": "input_text", "text": "Script failed (more output: wait with cell ID 7)\nWall time 0.1 seconds\nOutput:\n"},
			{"type": "input_text", "text": "page 1 stdout\n"},
		},
	}

	// Page 1 carries no Script error: yet, so no detail-less supplement is emitted.
	req1 := &codexresponses.Request{Fields: map[string]json.RawMessage{
		"input": responses.MustMarshalJSON([]any{execCall, page1Output}),
	}}
	preparer.observeCodeModeFailures("thread-1", "exec", req1, nil)
	if _, events := audience.Take("thread-1"); len(events) != 0 {
		t.Fatalf("expected no premature supplement on page 1, got %+v", events)
	}

	// Intermediate wait page still has more output and no error detail yet.
	wait1Call := map[string]any{
		"type":      "function_call",
		"call_id":   "call-wait-1",
		"name":      "wait",
		"arguments": `{"cell_id":"7"}`,
	}
	wait1Output := map[string]any{
		"type":    "function_call_output",
		"call_id": "call-wait-1",
		"output": []map[string]string{
			{"type": "input_text", "text": "Script failed (more output: wait with cell ID 7)\nWall time 0.1 seconds\nOutput:\n"},
			{"type": "input_text", "text": "page 2 stdout\n"},
		},
	}
	req2 := &codexresponses.Request{Fields: map[string]json.RawMessage{
		"input": responses.MustMarshalJSON([]any{execCall, page1Output, wait1Call, wait1Output}),
	}}
	preparer.observeCodeModeFailures("thread-1", "exec", req2, nil)
	if _, events := audience.Take("thread-1"); len(events) != 0 {
		t.Fatalf("expected no premature supplement on intermediate wait page, got %+v", events)
	}

	// Final wait page delivers Script error: and emits the detailed annotation on the exec call.
	wait2Call := map[string]any{
		"type":      "function_call",
		"call_id":   "call-wait-2",
		"name":      "wait",
		"arguments": `{"cell_id":"7"}`,
	}
	wait2Output := map[string]any{
		"type":    "function_call_output",
		"call_id": "call-wait-2",
		"output": []map[string]string{
			{"type": "input_text", "text": "Script failed\nWall time 0.1 seconds\nOutput:\n"},
			{"type": "input_text", "text": "Script error:\nTypeError: boom"},
		},
	}
	req3 := &codexresponses.Request{Fields: map[string]json.RawMessage{
		"input": responses.MustMarshalJSON([]any{execCall, page1Output, wait1Call, wait1Output, wait2Call, wait2Output}),
	}}
	preparer.observeCodeModeFailures("thread-1", "exec", req3, nil)
	_, events := audience.Take("thread-1")
	if len(events) != 1 || events[0].CallID != "call-exec-1" || events[0].Text != "exec script failed: TypeError: boom" {
		t.Fatalf("paged failure supplement = %+v, want single detailed supplement on call-exec-1", events)
	}

	// A stray wait not-found after a completed paged cell does not annotate the completed cell as failed.
	okCall := map[string]any{
		"type":    "custom_tool_call",
		"call_id": "call-exec-ok",
		"name":    "exec",
		"input":   "text('ok')",
	}
	okPage1 := map[string]any{
		"type":    "custom_tool_call_output",
		"call_id": "call-exec-ok",
		"output": []map[string]string{
			{"type": "input_text", "text": "Script completed (more output: wait with cell ID 9)\nWall time 0.1 seconds\nOutput:\n"},
			{"type": "input_text", "text": "ok"},
		},
	}
	strayWaitCall := map[string]any{
		"type":      "function_call",
		"call_id":   "call-wait-stray",
		"name":      "wait",
		"arguments": `{"cell_id":"9"}`,
	}
	strayWaitOutput := map[string]any{
		"type":    "function_call_output",
		"call_id": "call-wait-stray",
		"output": []map[string]string{
			{"type": "input_text", "text": "Script failed\nWall time 0.0 seconds\nOutput:\n"},
			{"type": "input_text", "text": "Script error:\nexec cell 9 not found"},
		},
	}
	req4 := &codexresponses.Request{Fields: map[string]json.RawMessage{
		"input": responses.MustMarshalJSON([]any{okCall, okPage1, strayWaitCall, strayWaitOutput}),
	}}
	preparer.observeCodeModeFailures("thread-1", "exec", req4, nil)
	if _, events := audience.Take("thread-1"); len(events) != 0 {
		t.Fatalf("expected no failure supplement for stray wait on completed cell, got %+v", events)
	}
}

func TestObserveCodeModeFailuresEmitsFallbackWhenPagedFailureAbandoned(t *testing.T) {
	audience := sessioncontrol.NewAudience(nil)
	audience.Attach("thread-1")
	audience.Observe("thread-1", "", "/root", false)
	preparer := &Preparer{Config: Config{Audience: audience}}

	execCall := map[string]any{
		"type":    "custom_tool_call",
		"call_id": "call-exec-abandoned",
		"name":    "exec",
		"input":   "throw new Error('boom')",
	}
	page1Output := map[string]any{
		"type":    "custom_tool_call_output",
		"call_id": "call-exec-abandoned",
		"output": []map[string]string{
			{"type": "input_text", "text": "Script failed (more output: wait with cell ID 11)\nWall time 0.1 seconds\nOutput:\n"},
			{"type": "input_text", "text": "page 1 stdout\n"},
		},
	}
	otherCall := map[string]any{
		"type":      "function_call",
		"call_id":   "call-other",
		"name":      "exec_command",
		"arguments": `{"cmd":"echo ok"}`,
	}
	otherOutput := map[string]any{
		"type":    "function_call_output",
		"call_id": "call-other",
		"output":  "Wall time: 0.0 seconds\nProcess exited with code 0\nOutput:\nok\n",
	}
	req := &codexresponses.Request{Fields: map[string]json.RawMessage{
		"input": responses.MustMarshalJSON([]any{execCall, page1Output, otherCall, otherOutput}),
	}}
	preparer.observeCodeModeFailures("thread-1", "exec", req, nil)
	_, events := audience.Take("thread-1")
	if len(events) != 1 || events[0].CallID != "call-exec-abandoned" || events[0].Text != "exec script failed" {
		t.Fatalf("abandoned paged failure supplement = %+v, want fallback on call-exec-abandoned", events)
	}
}

func TestConfirmsReportRejectsPagedSuffix(t *testing.T) {
	h := mekugiHistory{CarrierName: "exec", Report: "done"}
	completeOne := responses.MustMarshalJSON("Script completed\nWall time 0.1 seconds\nOutput:\ndone")
	completeTwo := responses.MustMarshalJSON([]map[string]string{
		{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"},
		{"type": "input_text", "text": "done"},
	})
	pagedOne := responses.MustMarshalJSON("Script completed (more output: wait with cell ID 7)\nWall time 0.1 seconds\nOutput:\ndone")
	pagedTwo := responses.MustMarshalJSON([]map[string]string{
		{"type": "input_text", "text": "Script completed (more output: wait with cell ID 7)\nWall time 0.1 seconds\nOutput:\n"},
		{"type": "input_text", "text": "done"},
	})
	if !h.confirmsReport(completeOne) || !h.confirmsReport(completeTwo) {
		t.Fatalf("expected unpaged completed reports to confirm")
	}
	if h.confirmsReport(pagedOne) || h.confirmsReport(pagedTwo) {
		t.Fatalf("expected paged completed reports with continuation suffix not to confirm")
	}
}
