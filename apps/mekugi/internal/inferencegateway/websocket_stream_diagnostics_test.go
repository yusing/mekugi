package inferencegateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestStreamDiagnosticsNativeDownstreamClose(t *testing.T) {
	messages := make(chan webSocketMessage, 1)
	closeErr := websocket.CloseError{Code: websocket.StatusGoingAway, Reason: "private downstream"}
	messages <- webSocketMessage{err: closeErr}
	var observed error
	var observedOrigin string
	exchange := &Exchange{
		ctx: t.Context(), ReadEnd: func(err error, origin string) { observed, observedOrigin = err, origin },
		session: &responsesWebSocket{provider: newTestClient("http://unused.invalid", nil), clientMessages: messages},
	}
	_, err := io.Copy(io.Discard, exchange)
	if !errors.Is(err, closeErr) || !errors.Is(observed, closeErr) || observedOrigin != "downstream" || websocket.CloseStatus(observed) != 1001 {
		t.Fatalf("downstream misattributed: %s, %v, %v", observedOrigin, observed, err)
	}
}

func TestStreamDiagnosticsNativeReaderShutdown(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		for _, origin := range []string{"upstream", "downstream"} {
			t.Run(fmt.Sprintf("%s/canceled_%t", origin, canceled), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				messages := make(chan webSocketMessage)
				close(messages)
				if canceled {
					cancel()
				}
				var observed error
				var observedOrigin string
				session := &responsesWebSocket{ctx: ctx, provider: newTestClient("http://unused.invalid", nil)}
				if origin == "upstream" {
					session.providerMessages = messages
				} else {
					session.clientMessages = messages
				}
				exchange := &Exchange{ctx: ctx, session: session, ReadEnd: func(err error, origin string) { observed, observedOrigin = err, origin }}
				_, err := io.Copy(io.Discard, exchange)
				if canceled {
					if !errors.Is(err, context.Canceled) || errors.Is(observed, io.EOF) {
						t.Fatalf("reader cancellation became EOF: %v, %v", observed, err)
					}
				} else {
					diagnostic, ok := errors.AsType[*testDiagnosticError](err)
					if !ok || diagnostic.Code != "websocket_reader_closed" || observedOrigin != origin {
						t.Fatalf("reader closure lost its origin: %s, %v", observedOrigin, err)
					}
				}
			})
		}
	}
}

func TestNativeReaderPreservesCloseErrorWhenDisconnectCancels(t *testing.T) {
	for range 10 {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			_ = conn.Close(websocket.StatusGoingAway, "private peer reason")
		}))
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(upstream.URL, "http"), nil)
		if err != nil {
			cancel()
			upstream.Close()
			t.Fatal(err)
		}
		disconnected := make(chan struct{})
		messages := readResponsesWebSocket(ctx, conn, func() {
			cancel()
			close(disconnected)
		})
		select {
		case <-disconnected:
		case <-ctx.Done():
		}
		message, open := <-messages
		conn.CloseNow()
		upstream.Close()
		if !open || websocket.CloseStatus(message.err) != websocket.StatusGoingAway {
			t.Fatalf("actual close was replaced by a closed channel: open=%v err=%v", open, message.err)
		}
		cancel()
	}
}
