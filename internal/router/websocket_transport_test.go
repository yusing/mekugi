package router

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

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
