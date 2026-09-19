package capturer

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/tokenizer"
)

func TestContentTokensIgnoreJSONFraming(t *testing.T) {
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	variants := []string{
		`{"input":"<hello>&\nworld","tools":[{"name":"lookup"}],"n":1000}`,
		" { \"n\": 1.0e3, \"tools\": [ { \"name\" : \"lookup\" } ], \"input\" : \"\\u003chello\\u003e\\u0026\\nworld\" } ",
	}
	recorder := &Recorder{codec: codec}
	want, err := recorder.measure([]byte(variants[0]))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range variants[1:] {
		got, err := recorder.measure([]byte(value))
		if err != nil {
			t.Fatal(err)
		}
		if got.Tokens != want.Tokens || got.Bytes == want.Bytes {
			t.Fatalf("framing changed tokens or hid wire bytes: %v vs %v", got, want)
		}
	}
	literal, _ := contentTokens([]byte(`{"input":"<"}`), codec)
	escapedText, _ := contentTokens([]byte(`{"input":"\\u003c"}`), codec)
	if escapedText <= literal {
		t.Fatalf("literal model-visible escape overhead lost: %d <= %d", escapedText, literal)
	}
	for _, value := range []string{"1", "1.0", "0.1e1", "100e-2"} {
		if got := normalizedNumber(value); got != "1" {
			t.Fatalf("normalize %s = %s", value, got)
		}
	}
}

func TestContentTokensIgnoreSSEAndOutputItemJSONFraming(t *testing.T) {
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	a := `{"type":"function_call","call_id":"call","name":"lookup","arguments":"<x>"}`
	b := ` { "arguments" : "\u003cx\u003e", "name":"lookup", "call_id":"call", "type":"function_call" } `
	var records [2]captureRecord
	for index, body := range []string{a, b} {
		observeOutputItem([]byte(body), &records[index], codec)
	}
	if len(records[0].ToolCalls) != 1 || len(records[1].ToolCalls) != 1 {
		t.Fatal("missing tool")
	}
	left, right := records[0].ToolCalls[0], records[1].ToolCalls[0]
	if left.ItemTokens != right.ItemTokens || left.InputTokens != right.InputTokens || left.ItemBytes == right.ItemBytes {
		t.Fatalf("tool counts %v vs %v", left, right)
	}
	plain, _ := contentTokens([]byte(a), codec)
	stream, _ := contentTokens([]byte("event: anything\r\ndata: "+strings.Replace(b, ",", ",\r\ndata:", 1)+"\r\n\r\ndata: [DONE]\r\n\r\n"), codec)
	if plain != stream {
		t.Fatalf("SSE framing counted: %d != %d", plain, stream)
	}
}

func TestOutputTextCompressionExcludesToolTranslation(t *testing.T) {
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	small := []byte(`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"<same text>"}]},{"type":"custom_tool_call","input":"short"}]`)
	large := []byte(`[{"type":"function_call","arguments":"a much longer translated command with many extra words"},{"role":"assistant","type":"message","content":[{"text":"\u003csame text\u003e","type":"output_text"}]}]`)
	left, err := measureOutputText(small, codec)
	if err != nil {
		t.Fatal(err)
	}
	right, err := measureOutputText(large, codec)
	if err != nil {
		t.Fatal(err)
	}
	if left != right || left.Tokens == 0 {
		t.Fatalf("tool delivery affected CTP text savings: %v != %v", left, right)
	}
}

func TestContentTokensIgnoreSSEBOM(t *testing.T) {
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	event := []byte("data: {\"content\":\"hello\"}\n\n")
	plain, err := contentTokens(event, codec)
	if err != nil {
		t.Fatal(err)
	}
	bom, err := contentTokens(append([]byte{0xef, 0xbb, 0xbf}, event...), codec)
	if err != nil {
		t.Fatal(err)
	}
	if plain == 0 || bom != plain {
		t.Fatalf("BOM changed content estimate: %d != %d", bom, plain)
	}
}
