package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
)

func TestResponsesWebSocketQueuesCreateWithoutProviderSocket(t *testing.T) {
	s := &responsesWebSocket{ctx: t.Context()}
	create := []byte(`{"type":"response.create","model":"grok-4","input":"next"}`)
	if err := s.control(create); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s.queuedCreate, create) {
		t.Fatalf("queued create = %s", s.queuedCreate)
	}
	if err := s.control(create); err == nil {
		t.Fatal("duplicate continuation was accepted")
	}
	s.queuedCreate = nil
	if err := s.control([]byte(`{"type":"response.steer","input":"change"}`)); err == nil {
		t.Fatal("steering without a provider socket was accepted")
	}
}

func TestResponsesWebSocketTerminalOutputBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	conn, _, err := websocket.Dial(t.Context(), server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	streamed := json.RawMessage(`{"type":"message","id":"item","text":"streamed"}`)
	replacement := json.RawMessage(`{"type":"message","id":"item","text":"final metadata"}`)
	commentary := json.RawMessage(`{"type":"message","id":"commentary","text":"progress"}`)
	for _, tc := range []struct {
		name      string
		output    []json.RawMessage
		want      []json.RawMessage
		remaining int
		wantError bool
	}{
		{"same output at budget", []json.RawMessage{streamed}, []json.RawMessage{streamed}, 0, false},
		{"matching item updates metadata", []json.RawMessage{replacement}, []json.RawMessage{replacement}, len(replacement) - len(streamed), false},
		{"terminal-only addition preserves streamed", []json.RawMessage{commentary}, []json.RawMessage{streamed, commentary}, len(commentary), false},
		{"partial snapshot updates and preserves", []json.RawMessage{commentary, replacement}, []json.RawMessage{replacement, commentary}, len(commentary) + len(replacement) - len(streamed), false},
		{"addition exceeds budget", []json.RawMessage{commentary}, []json.RawMessage{streamed}, 0, true},
		{"empty preserves streamed", nil, []json.RawMessage{streamed}, 0, false},
		{"replacement exceeds budget", []json.RawMessage{replacement}, []json.RawMessage{streamed}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := &webSocketHistory{output: []json.RawMessage{streamed}}
			initial := upstreamJSONBufferBytes - tc.remaining
			s := &responsesWebSocket{downstream: conn, retainedBytes: initial, histories: make(map[string]*webSocketHistory)}
			output := &webSocketOutput{exchange: &webSocketExchange{session: s, ctx: t.Context(), history: history}}
			err := output.message(mustMarshalJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": "done", "output": tc.output}}))
			if (err != nil) != tc.wantError {
				t.Fatalf("terminal error = %v", err)
			}
			if !bytes.Equal(mustMarshalJSON(history.output), mustMarshalJSON(tc.want)) {
				t.Fatalf("retained output = %s", mustMarshalJSON(history.output))
			}
			wantBytes := initial
			if !tc.wantError {
				wantBytes -= len(streamed)
				for _, item := range tc.want {
					wantBytes += len(item)
				}
			}
			if s.retainedBytes != wantBytes {
				t.Fatalf("retained bytes = %d, want %d", s.retainedBytes, wantBytes)
			}
		})
	}
}
