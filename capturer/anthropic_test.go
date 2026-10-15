package capturer

import (
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/tokenizer"
)

func TestAnthropicCaptureActualToolAndTerminal(t *testing.T) {
	codec, err := tokenizer.New()
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
	output := observeAnthropicResponse([]byte(stream), &record)
	if len(output) == 0 || record.ResponseStatus != "completed" ||
		record.ProviderResponse.Model != "minimax-m3" {
		t.Fatalf("Messages capture lost actual output: %+v", record)
	}
	var messages []struct {
		Content []struct {
			Type  string `json:"type"`
			ID    string `json:"id"`
			Name  string `json:"name"`
			Input struct {
				Input string `json:"input"`
			} `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(output, &messages); err != nil || len(messages) != 1 || len(messages[0].Content) != 4 {
		t.Fatalf("reconstructed Messages output=%s error=%v", output, err)
	}
	tool := messages[0].Content[0]
	if tool.Type != "tool_use" || tool.ID != "c1" || tool.Name != "exec" || tool.Input.Input != "exact" {
		t.Fatalf("reconstructed Messages tool=%+v", tool)
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
	if output := observeAnthropicResponse([]byte(truncated), &record); len(output) != 0 || record.ResponseStatus != "" {
		t.Fatal("truncated Messages capture completed")
	}
}
