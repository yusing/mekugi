package capturer

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestChatCaptureTerminalOutputAndCompletion(t *testing.T) {
	stream := "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"exec","arguments":"{\"input\":"}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"text(1)\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	var record captureRecord
	output := observeChatResponse([]byte(stream), "text/event-stream", &record)
	if record.ResponseStatus != "completed" {
		t.Fatalf("record=%+v output=%s", record, output)
	}
	var messages []struct {
		ToolCalls []chatCaptureCall `json:"tool_calls"`
	}
	if err := json.Unmarshal(output, &messages); err != nil || len(messages) != 1 || len(messages[0].ToolCalls) != 1 {
		t.Fatalf("reconstructed tool output=%s error=%v", output, err)
	}
	call := messages[0].ToolCalls[0]
	if call.ID != "c1" || call.Function.Name != "exec" || call.Function.Arguments != `{"input":"text(1)"}` {
		t.Fatalf("reconstructed tool call=%+v", call)
	}
	record = captureRecord{}
	if output := observeChatResponse([]byte(strings.TrimSuffix(stream, "data: [DONE]\n\n")), "text/event-stream", &record); output != nil || record.ResponseStatus != "" {
		t.Fatal("truncated chat completion was counted as completed")
	}
	names := requestToolNames([]jsonv1.RawMessage{jsonv1.RawMessage(`{"type":"function","function":{"name":"exec"}}`)})
	if len(names) != 1 || names[0] != "exec" {
		t.Fatalf("names=%v", names)
	}
}

func TestChatCaptureSSELineEndingsAndBOM(t *testing.T) {
	// Multiline data must remain a single event, including with CRLF framing.
	stream := "data: {\"choices\":[\ndata: {\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		for _, bom := range []string{"", "\xef\xbb\xbf"} {
			for _, contentType := range []string{"text/event-stream", ""} {
				var record captureRecord
				output := observeChatResponse([]byte(bom+strings.ReplaceAll(stream, "\n", ending)), contentType, &record)
				if record.ResponseStatus != "completed" || record.CaptureError != "" || string(output) != `[{"content":"ok","role":"assistant"}]` {
					t.Fatalf("ending=%q BOM=%q type=%q record=%+v output=%s", ending, bom, contentType, record, output)
				}
			}
		}
	}
}

func TestChatCaptureFinishEvidence(t *testing.T) {
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
			output := observeChatResponse([]byte(payload), contentType, &record)
			if record.ResponseStatus != test.status || (len(output) != 0) != (test.status != "") {
				t.Fatalf("reason=%q stream=%v status=%q output=%s", test.reason, stream, record.ResponseStatus, output)
			}
		}
	}
}
