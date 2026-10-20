package inferencegateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/coder/websocket"
)

func TestWebSocketOutputDisconnectBeforeReaderCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
	}))
	defer server.Close()
	client, _, err := websocket.Dial(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	var downstream *websocket.Conn
	select {
	case downstream = <-accepted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer downstream.CloseNow()
	output := &webSocketOutput{exchange: &Exchange{
		State: confirmedHistoryExchange(t, nil, false),
		ctx:   ctx, session: &responsesWebSocket{downstream: downstream},
	}}
	// A successful event must count as response-started for WebSocket output.
	read := make(chan error, 1)
	go func() {
		_, _, err := client.Read(ctx)
		read <- err
	}()
	event := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
	if _, err := output.Write(event); err != nil {
		t.Fatal(err)
	}
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	if !output.ResponseStarted() {
		t.Fatal("WebSocket delivery was not tracked")
	}
	// No downstream reader runs here: closing the socket cannot cancel ctx.
	if err := downstream.CloseNow(); err != nil {
		t.Fatal(err)
	}
	_, err = output.Write(event)
	if !errors.Is(err, codexresponses.ErrDownstreamDisconnected) || ctx.Err() != nil {
		t.Fatalf("write error=%v, context=%v", err, ctx.Err())
	}
}
