package openai

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestWebSocketNonstreamTerminalReconstruction(t *testing.T) {
	for _, kind := range []string{"completed", "failed", "incomplete"} {
		for _, status := range []string{"", "retained"} {
			for _, snapshot := range []bool{false, true} {
				t.Run(kind+"/"+status+"/"+map[bool]string{false: "reconstruct", true: "snapshot"}[snapshot], func(t *testing.T) {
					terminal := map[string]any{"id": "response"}
					if status != "" {
						terminal["status"] = status
					}
					if snapshot {
						terminal["output"] = []any{map[string]string{"id": "snapshot"}}
					}
					body := &WebSocketBody{prefetched: [][]byte{
						[]byte(`{"type":"response.output_item.done","output_index":2,"item":{"id":"last"}}`),
						[]byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"first"}}`),
						responses.MustMarshalJSON(map[string]any{"type": "response." + kind, "response": terminal}),
					}}
					raw, err := io.ReadAll(body)
					if err != nil {
						t.Fatal(err)
					}
					var got struct {
						Status string                `json:"status"`
						Output []struct{ ID string } `json:"output"`
					}
					if err := json.Unmarshal(raw, &got); err != nil {
						t.Fatal(err)
					}
					wantStatus := status
					if wantStatus == "" {
						wantStatus = kind
					}
					if got.Status != wantStatus {
						t.Fatalf("status = %q, want %q", got.Status, wantStatus)
					}
					if snapshot {
						if len(got.Output) != 1 || got.Output[0].ID != "snapshot" {
							t.Fatalf("snapshot replaced: %s", raw)
						}
					} else if len(got.Output) != 2 || got.Output[0].ID != "first" || got.Output[1].ID != "last" {
						t.Fatalf("reconstruction order: %s", raw)
					}
				})
			}
		}
	}
	for _, response := range []string{"null", "[]", `"invalid"`} {
		body := &WebSocketBody{prefetched: [][]byte{[]byte(`{"type":"response.completed","response":` + response + `}`)}}
		if _, err := io.ReadAll(body); err == nil {
			t.Fatalf("accepted invalid terminal %s", response)
		}
	}
}

func BenchmarkWebSocketNonstreamReconstruction(b *testing.B) {
	item := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"item","text":"` + strings.Repeat("text ", 1000) + `"}}`)
	terminal := []byte(`{"type":"response.completed","response":{"id":"response","output":[]}}`)
	b.ReportAllocs()
	for b.Loop() {
		body := &WebSocketBody{prefetched: [][]byte{item, terminal}}
		if _, err := io.Copy(io.Discard, body); err != nil {
			b.Fatal(err)
		}
	}
}
