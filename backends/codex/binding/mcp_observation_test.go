package binding

import (
	"bytes"
	"encoding/json/jsontext"
	"testing"
	"time"
)

func TestMCPObservationPreservesHostEvidence(t *testing.T) {
	raw := jsontext.Value(`{"threadId":"child","turnId":"turn","item":{"type":"mcpToolCall","id":"call","server":"catalog","tool":"lookup","status":"failed","arguments":{ "id": 9007199254740993 },"result":{"content":[{"type":"image","data":"AA==","mimeType":"image/png"}],"structuredContent":{"ok":false},"_meta":{"key":"value"}},"error":{"message":"lookup failed"},"durationMs":123}}`)
	original := bytes.Clone(raw)
	p, err := DecodeNotification("item/completed", raw)
	if err != nil {
		t.Fatal(err)
	}
	e := p.Observation("item/completed", time.Unix(100, 0))
	if e.ThreadID != "child" || e.TurnID != "turn" || e.ItemID != "call" || e.Item.Server != "catalog" || e.Item.Tool != "lookup" || e.Item.Status != "failed" || *e.Item.DurationMS != 123 {
		t.Fatalf("lost identity/outcome: %+v", e)
	}
	if string(e.Item.Arguments) != `{ "id": 9007199254740993 }` || !bytes.Equal(e.Item.Result, p.Item.Result) || !bytes.Equal(e.Item.Error, p.Item.Error) || !bytes.Equal(raw, original) {
		t.Fatal("host evidence changed")
	}
	e.Item.Arguments[0], e.Item.Result[0], e.Item.Error[0] = 'x', 'x', 'x'
	if !p.Item.Arguments.IsValid() || !p.Item.Result.IsValid() || !p.Item.Error.IsValid() {
		t.Fatal("observation aliases native payload")
	}
}
