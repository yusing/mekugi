package capturer

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/tokenizer"
)

func TestChatCaptureRecordsActualToolShapeAndCompletion(t *testing.T) {
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	stream := "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"exec","arguments":"{\"input\":"}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"text(1)\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	var record captureRecord
	output := observeChatResponse([]byte(stream), "text/event-stream", &record, codec)
	if record.ResponseStatus != "completed" || len(record.ToolCalls) != 1 || record.ToolCalls[0].CallID != "c1" || record.ToolCalls[0].Name != "exec" || !json.Valid(output) {
		t.Fatalf("record=%+v output=%s", record, output)
	}
	record = captureRecord{}
	if output := observeChatResponse([]byte(strings.TrimSuffix(stream, "data: [DONE]\n\n")), "text/event-stream", &record, codec); output != nil || record.ResponseStatus != "" {
		t.Fatal("truncated chat completion was counted as completed")
	}
	names := requestToolNames([]json.RawMessage{json.RawMessage(`{"type":"function","function":{"name":"exec"}}`)})
	if len(names) != 1 || names[0] != "exec" {
		t.Fatalf("names=%v", names)
	}
}

func TestChatCaptureSSELineEndingsAndBOM(t *testing.T) {
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	// Multiline data must remain a single event, including with CRLF framing.
	stream := "data: {\"choices\":[\ndata: {\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		for _, bom := range []string{"", "\xef\xbb\xbf"} {
			for _, contentType := range []string{"text/event-stream", ""} {
				var record captureRecord
				output := observeChatResponse([]byte(bom+strings.ReplaceAll(stream, "\n", ending)), contentType, &record, codec)
				if record.ResponseStatus != "completed" || record.CaptureError != "" || string(output) != `[{"content":"ok","role":"assistant"}]` {
					t.Fatalf("ending=%q BOM=%q type=%q record=%+v output=%s", ending, bom, contentType, record, output)
				}
			}
		}
	}
}

func TestChatCaptureDecodedToolItemTokens(t *testing.T) {
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	call := `{"id":"c1","type":"function","function":{"name":"exec","arguments":"{\"input\":\"quoted \\\"text\\\"\\nnext\"}"}}`
	for _, stream := range []bool{false, true} {
		payload := `{"choices":[{"message":{"tool_calls":[` + call + `]},"finish_reason":"tool_calls"}]}`
		kind := "application/json"
		if stream {
			payload = `data: {"choices":[{"delta":{"tool_calls":[{"index":0,` + strings.TrimPrefix(call, "{") + `]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
			kind = "text/event-stream"
		}
		var record captureRecord
		observeChatResponse([]byte(payload), kind, &record, codec)
		want, err := contentTokens([]byte(call), codec)
		if err != nil {
			t.Fatal(err)
		}
		if len(record.ToolCalls) != 1 || record.ToolCalls[0].ItemTokens != uint64(want) {
			t.Fatalf("stream=%v calls=%+v want=%d error=%s", stream, record.ToolCalls, want, record.CaptureError)
		}
		var decoded chatCaptureCall
		if err := json.Unmarshal([]byte(call), &decoded); err != nil {
			t.Fatal(err)
		}
		input, _ := codec.Count(decoded.Function.Arguments)
		if record.ToolCalls[0].InputTokens != uint64(input) || record.ToolCalls[0].InputBytes != uint64(len(decoded.Function.Arguments)) {
			t.Fatalf("input metrics = %+v", record.ToolCalls[0])
		}
	}
}

func TestChatCaptureFinishEvidence(t *testing.T) {
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ reason, status string }{
		{"stop", "completed"}, {"tool_calls", "completed"},
		{"length", "incomplete"}, {"content_filter", "incomplete"},
		{"", ""}, {"future", ""},
	} {
		for _, stream := range []bool{false, true} {
			payload := `{"choices":[{"message":{"content":"answer"},"finish_reason":"` + test.reason + `"}]}`
			contentType := "application/json"
			if stream {
				payload = "data: " + strings.Replace(payload, `"message":`, `"delta":`, 1) + "\n\ndata: [DONE]\n\n"
				contentType = "text/event-stream"
			}
			var record captureRecord
			output := observeChatResponse([]byte(payload), contentType, &record, codec)
			if record.ResponseStatus != test.status || (len(output) != 0) != (test.status != "") {
				t.Fatalf("reason=%q stream=%v status=%q output=%s", test.reason, stream, record.ResponseStatus, output)
			}
		}
	}
}
