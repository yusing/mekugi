package router

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Source: codex-rs/core/tests/suite/pending_input.rs:1137:1217@68e1a421
// steer_interrupts_and_drains_websocket. Interrupt is host control, not a
// router-owned cancellation or an automatic steering successor.
func TestResponsesWebSocketInstantInterrupt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	interrupt := []byte(`{"type":"response.interrupt","response_id":"parent","mode":"discard_partial_items"}`)
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.CloseNow()
		if _, err := providerSocketRead(ctx, upstream); err != nil {
			t.Error(err)
			return
		}
		if err := providerSocketWrite(ctx, upstream, socketEvent("response.created", "parent")); err != nil {
			t.Error(err)
			return
		}
		_, body, err := upstream.Read(ctx)
		if err != nil || !bytes.Equal(body, interrupt) {
			t.Errorf("host interrupt changed: %s, %v", body, err)
			return
		}
		for _, event := range []any{
			map[string]any{"type": "response.interrupt.accepted", "response_id": "parent", "sequence_number": 3},
			map[string]any{"type": "response.output_item.interrupted", "response_id": "parent", "item_id": "partial", "output_index": 0, "sequence_number": 4},
			map[string]any{"type": "response.incomplete", "response": map[string]any{"id": "parent", "status": "incomplete", "incomplete_details": map[string]string{"reason": "interrupted"}, "output": []any{}}},
		} {
			if err := providerSocketWrite(ctx, upstream, event); err != nil {
				t.Error(err)
				return
			}
		}
		next, err := providerSocketRead(ctx, upstream)
		if err != nil {
			t.Error(err)
			return
		}
		if jsonString(next, "type") != "response.create" || jsonString(next, "previous_response_id") != "parent" || !bytes.Contains(next["input"], []byte("second prompt")) || bytes.Contains(next["input"], []byte("first prompt")) {
			t.Errorf("interrupt or input replayed instead of host continuation: %s", mustMarshalJSON(next))
		}
		for _, kind := range []string{"response.created", "response.completed"} {
			if err := providerSocketWrite(ctx, upstream, socketEvent(kind, "next")); err != nil {
				t.Error(err)
				return
			}
		}
		_, _, _ = upstream.Read(ctx)
	}), nil, codexAuthHeaders())
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "input": "first prompt"})
	if got := jsonString(socketRead(t, ctx, conn), "type"); got != "response.created" {
		t.Fatalf("first event = %s", got)
	}
	if err := conn.Write(ctx, websocket.MessageText, interrupt); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"response.interrupt.accepted", "response.output_item.interrupted", "response.incomplete"} {
		if got := jsonString(socketRead(t, ctx, conn), "type"); got != kind {
			t.Fatalf("event = %s, want %s", got, kind)
		}
	}
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": "gpt-test", "previous_response_id": "parent", "input": "second prompt"})
	for _, kind := range []string{"response.created", "response.completed"} {
		if got := jsonString(socketRead(t, ctx, conn), "type"); got != kind {
			t.Fatalf("follow-up event = %s, want %s", got, kind)
		}
	}
}
