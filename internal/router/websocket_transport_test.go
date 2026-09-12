package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
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
					body := &webSocketResponseBody{prefetched: [][]byte{
						[]byte(`{"type":"response.output_item.done","output_index":2,"item":{"id":"last"}}`),
						[]byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"first"}}`),
						mustMarshalJSON(map[string]any{"type": "response." + kind, "response": terminal}),
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
		body := &webSocketResponseBody{prefetched: [][]byte{[]byte(`{"type":"response.completed","response":` + response + `}`)}}
		if _, err := io.ReadAll(body); err == nil {
			t.Fatalf("accepted invalid terminal %s", response)
		}
	}
}

func TestWebSocketOutputFragmentedWrites(t *testing.T) {
	received := make(chan []byte, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, payload, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			received <- payload
		}
	}))
	defer server.Close()
	conn, _, err := websocket.Dial(t.Context(), server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	w := &webSocketOutput{exchange: &webSocketExchange{
		ctx: t.Context(), session: &responsesWebSocket{downstream: conn},
	}}
	want := []byte("{\"type\":\"response.output_text.delta\",\n\"delta\":\"hello 世界\"}")
	event := ": ignored\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\ndata: \"delta\":\"hello 世界\"}\n\n"
	for _, width := range []int{1, 7, len(event) * 2} {
		input := []byte(event + event)
		for len(input) > 0 {
			n := min(width, len(input))
			part := bytes.Clone(input[:n])
			written, err := w.Write(part)
			if err != nil || written != n {
				t.Fatalf("write = %d, %v", written, err)
			}
			clear(part)
			input = input[n:]
		}
		for range 2 {
			select {
			case got := <-received:
				if !bytes.Equal(got, want) {
					t.Fatalf("payload = %s, want %s", got, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("missing websocket payload")
			}
		}
	}
	if w.buffer.Len() != 0 {
		t.Fatalf("unconsumed framing: %q", w.buffer.String())
	}
}

func BenchmarkWebSocketNonstreamReconstruction(b *testing.B) {
	item := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"item","text":"` + strings.Repeat("text ", 1000) + `"}}`)
	terminal := []byte(`{"type":"response.completed","response":{"id":"response","output":[]}}`)
	b.ReportAllocs()
	for b.Loop() {
		body := &webSocketResponseBody{prefetched: [][]byte{item, terminal}}
		if _, err := io.Copy(io.Discard, body); err != nil {
			b.Fatal(err)
		}
	}
}
