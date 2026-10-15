package providercodec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/responses"
)

const grokModel = "grok:grok-4.6"

type testDiagnostic struct{ code, summary, detail string }

func (e *testDiagnostic) Error() string {
	if e.detail != "" {
		return e.detail
	}
	return e.summary
}

func testPolicy() Policy {
	return Policy{
		Diagnostics: Diagnostics{
			Name: "Grok", Subject: "grok", Prefix: "grok", ContentErrorCode: "opencode_stream_invalid", BufferBytes: 64 << 20,
			Error: func(code, summary, detail string) error { return &testDiagnostic{code, summary, detail} },
			DescribeError: func(err error) (string, string, bool) {
				e, ok := errors.AsType[*testDiagnostic](err)
				if !ok {
					return "", "", false
				}
				return e.code, e.Error(), true
			},
			Incompatible: func(code, message string) error { return fmt.Errorf("Mekugi %s: %s", code, message) },
			ForwardError: func(err error) error { return err },
		},
		ResolveEffort: func(effort string) (string, error) { return effort, nil },
	}
}

func mustTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	return responses.MustMarshalJSON(value)
}

func testParse(body []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	err := json.Unmarshal(body, &fields)
	return fields, err
}

type testRequest struct {
	body  map[string]any
	codec *Stream
}

func testTranslate(body []byte) (*testRequest, error) {
	return testTranslateFormat(body, "chat")
}

func testTranslateFormat(body []byte, format string) (*testRequest, error) {
	fields, err := testParse(body)
	if err != nil {
		return nil, err
	}
	wire, codec, err := Translate(fields, "fixture-model", format, testPolicy())
	return &testRequest{wire, codec}, err
}

func grokTestRequest(t *testing.T, stream bool) []byte {
	t.Helper()
	return mustTestJSON(t, map[string]any{"stream": stream, "instructions": "test instructions", "reasoning": map[string]string{"effort": "low"}, "tools": []any{map[string]any{"type": "custom", "name": "exec", "description": "Run exact input"}}, "input": []any{map[string]any{"type": "agent_message", "content": []any{map[string]string{"type": "input_text", "text": "Run the probe"}}}}})
}

func grokTestSSE(chunks ...any) string {
	var out strings.Builder
	for _, chunk := range chunks {
		fmt.Fprintf(&out, "data: %s\n\n", responses.MustMarshalJSON(chunk))
	}
	out.WriteString("data: [DONE]\n\n")
	return out.String()
}

func grokTextStream() string {
	return grokTestSSE(map[string]any{"model": "grok-4.6-build", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": "GROK_OK"}, "finish_reason": "stop"}}}, map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4, "prompt_tokens_details": map[string]int{"cached_tokens": 3}, "completion_tokens_details": map[string]int{"reasoning_tokens": 2}}})
}

func assertCodecFailure(t *testing.T, stream *Stream, wire, code, message string) {
	t.Helper()
	var events []map[string]any
	result, err := stream.Read(strings.NewReader(wire), func(event map[string]any) error { events = append(events, event); return nil }, testFailureDetail)
	diagnostic, ok := errors.AsType[*testDiagnostic](err)
	if result != nil || !ok || diagnostic.code != code || err.Error() != message {
		t.Fatalf("result=%v error=%v; want %s: %s", result, err, code, message)
	}
	if len(events) < 2 || events[0]["type"] != "response.created" {
		t.Fatalf("missing failure lifecycle: %v", events)
	}
	last := events[len(events)-1]
	failed := last["response"].(map[string]any)
	if last["type"] != "response.failed" || failed["id"] != events[0]["response"].(map[string]any)["id"] || failed["error"].(map[string]string)["code"] != code || failed["error"].(map[string]string)["message"] != message {
		t.Fatalf("invalid failure terminal: %v", last)
	}
}

func testFailureDetail(body []byte) string { return string(body) }
