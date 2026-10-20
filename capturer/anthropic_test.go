package capturer

import (
	"strings"
	"testing"
)

func TestAnthropicCaptureTerminalAndModel(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"model":"minimax-m3"}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c1","name":"exec","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"exact\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":"answer"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"thinking_delta","thinking":"private thinking"}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"signature_delta","signature":"opaque signature"}}`,
		`data: {"type":"content_block_stop","index":2}`,
		`data: {"type":"content_block_start","index":3,"content_block":{"type":"redacted_thinking","data":"opaque redacted data"}}`,
		`data: {"type":"content_block_stop","index":3}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
		`data: {"type":"message_stop"}`,
	}, "\n\n") + "\n\n"
	record := captureRecord{ProviderResponse: &providerResponseEvidence{}}
	observeAnthropicResponse([]byte(stream), &record)
	if record.ResponseStatus != "completed" ||
		record.ProviderResponse.Model != "minimax-m3" {
		t.Fatalf("Messages capture lost terminal or model evidence: %+v", record)
	}
	record = captureRecord{}
	truncated := strings.TrimSuffix(stream, "data: {\"type\":\"message_stop\"}\n\n")
	observeAnthropicResponse([]byte(truncated), &record)
	if record.ResponseStatus != "" {
		t.Fatal("truncated Messages capture completed")
	}
}

func TestAnthropicCaptureBlockLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, events, status, captureError string
	}{
		{"open block", `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`, "", ""},
		{"duplicate block", `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`, "", "duplicate Messages content block"},
		{"orphan delta", `{"type":"content_block_delta","index":0,"delta":{"text":"x"}}`, "", "Messages delta has no active content block"},
		{"orphan stop", `{"type":"content_block_stop","index":0}`, "", "invalid Messages content completion"},
		{"provider error", `{"type":"error"}`, "failed", ""},
		{"malformed", `{`, "", "invalid Messages event JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := "data: " + tc.events + "\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
			var record captureRecord
			observeAnthropicResponse([]byte(stream), &record)
			if record.ResponseStatus != tc.status || record.CaptureError != tc.captureError {
				t.Fatalf("capture = %+v", record)
			}
		})
	}
}

func TestAnthropicCaptureRejectsMissingContentBlock(t *testing.T) {
	for _, block := range []string{"", `,"content_block":null`, `,"content_block":[]`, `,"content_block":42`} {
		stream := "data: " + `{"type":"content_block_start","index":0` + block + "}\n\n" +
			`data: {"type":"content_block_stop","index":0}` + "\n\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}` + "\n\n" +
			`data: {"type":"message_stop"}` + "\n\n"
		var record captureRecord
		observeAnthropicResponse([]byte(stream), &record)
		if record.CaptureError == "" || record.ResponseStatus != "" {
			t.Fatalf("invalid block %s: %+v", block, record)
		}
	}
}

func TestAnthropicCaptureRejectsMalformedDelta(t *testing.T) {
	for _, field := range []string{"text", "thinking", "signature", "partial_json"} {
		stream := `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"` + field + `":42}}` + "\n\n" +
			`data: {"type":"content_block_stop","index":0}` + "\n\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}` + "\n\n" +
			`data: {"type":"message_stop"}` + "\n\n"
		var record captureRecord
		observeAnthropicResponse([]byte(stream), &record)
		if record.CaptureError != "invalid Messages event JSON" || record.ResponseStatus != "" {
			t.Fatalf("invalid delta %s: %+v", field, record)
		}
	}
}
