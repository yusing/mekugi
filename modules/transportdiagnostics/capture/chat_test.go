package capture

import (
	jsonv1 "encoding/json"
	"strings"
	"testing"
)

func TestChatCaptureTerminalCompletion(t *testing.T) {
	stream := "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"exec","arguments":"{\"input\":"}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"text(1)\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	var record captureRecord
	observeChatResponse([]byte(stream), "text/event-stream", &record)
	if record.ResponseStatus != "completed" {
		t.Fatalf("record=%+v", record)
	}
	record = captureRecord{}
	observeChatResponse([]byte(strings.TrimSuffix(stream, "data: [DONE]\n\n")), "text/event-stream", &record)
	if record.ResponseStatus != "" {
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
				observeChatResponse([]byte(bom+strings.ReplaceAll(stream, "\n", ending)), contentType, &record)
				if record.ResponseStatus != "completed" || record.CaptureError != "" {
					t.Fatalf("ending=%q BOM=%q type=%q record=%+v", ending, bom, contentType, record)
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
			observeChatResponse([]byte(payload), contentType, &record)
			if record.ResponseStatus != test.status {
				t.Fatalf("reason=%q stream=%v status=%q", test.reason, stream, record.ResponseStatus)
			}
		}
	}
}

func TestChatCaptureRejectsMalformedContentEvidence(t *testing.T) {
	for _, field := range []string{`"content":42`, `"tool_calls":42`, `"tool_calls":[{"function":{"arguments":42}}]`} {
		for _, member := range []string{"message", "delta"} {
			for _, stream := range []bool{false, true} {
				payload := `{"service_tier":"priority","choices":[{"` + member + `":{` + field + `},"finish_reason":"stop"}]}`
				kind := "application/json"
				if stream {
					payload = "data: " + payload + "\n\ndata: [DONE]\n\n"
					kind = "text/event-stream"
				}
				record := captureRecord{Boundary: "provider", ProviderResponse: &providerResponseEvidence{}}
				observeChatResponse([]byte(payload), kind, &record)
				if record.CaptureError == "" || record.ResponseStatus != "" || record.ProviderResponse.ServiceTier != "" {
					t.Fatalf("malformed %s %s stream=%v: %+v evidence=%+v", member, field, stream, record, record.ProviderResponse)
				}
			}
		}
	}
}
