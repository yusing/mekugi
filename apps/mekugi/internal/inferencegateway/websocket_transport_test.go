package inferencegateway

import (
	"bytes"
	"encoding/json/jsontext"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
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
	w := &webSocketOutput{exchange: &Exchange{
		State: confirmedHistoryExchange(t, nil, false),
		ctx:   t.Context(), session: &responsesWebSocket{downstream: conn},
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

func confirmedHistoryExchange(t *testing.T, input []jsontext.Value, automatic bool) *codexresponses.SessionExchange {
	t.Helper()
	var session codexresponses.SessionHistory
	parent, request, err := session.Prepare(responses.MustMarshalJSON(map[string]any{"model": "gpt-test", "input": input}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.BeginProvider(request.Fields); err != nil {
		t.Fatal(err)
	}
	terminal := []byte(`{"type":"response.completed","response":{"id":"parent","output":[]}}`)
	if err := parent.ObserveProvider(terminal); err != nil {
		t.Fatal(err)
	}
	if err := parent.ObserveOutput(terminal); err != nil {
		t.Fatal(err)
	}
	command := responses.MustMarshalJSON(map[string]any{"previous_response_id": "parent", "input": []any{}})
	var first []byte
	if automatic {
		command = nil
		first = []byte(`{"type":"response.created","response":{"id":"next","previous_response_id":"parent"}}`)
	}
	exchange, _, err := session.Prepare(command, first)
	if err != nil {
		t.Fatal(err)
	}
	return exchange
}
