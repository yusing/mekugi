package capturer

import (
	"strings"
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

func TestAnthropicCaptureActualToolAndTerminal(t *testing.T) {
	codec, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatal(err)
	}
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
	output := observeAnthropicResponse([]byte(stream), &record, codec)
	if len(output) == 0 || record.ResponseStatus != "completed" || len(record.ToolCalls) != 1 ||
		record.ToolCalls[0].CallID != "c1" || record.ToolCalls[0].Name != "exec" || record.ProviderResponse.Model != "minimax-m3" {
		t.Fatalf("Messages capture lost actual output: %+v", record)
	}
	text, err := measureOutputText(output, codec)
	if err != nil || text.Bytes != uint64(len("answer")) || text.Tokens == 0 {
		t.Fatalf("Messages final text accounting: %+v, %v", text, err)
	}
	for _, retained := range []string{"private thinking", "opaque signature", "opaque redacted data"} {
		if !strings.Contains(string(output), retained) {
			t.Fatalf("model-origin aggregate output lost %q", retained)
		}
	}
	record = captureRecord{}
	truncated := strings.TrimSuffix(stream, "data: {\"type\":\"message_stop\"}\n\n")
	if output := observeAnthropicResponse([]byte(truncated), &record, codec); len(output) != 0 || record.ResponseStatus != "" {
		t.Fatal("truncated Messages capture completed")
	}
}
