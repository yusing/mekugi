package inferencegateway

import (
	"errors"
	"testing"
)

func TestStreamDiagnosticsRejectedWebSocketControl(t *testing.T) {
	for _, kind := range []string{"response.create", "response.steer"} {
		t.Run(kind, func(t *testing.T) {
			messages := make(chan webSocketMessage, 1)
			messages <- webSocketMessage{body: []byte(`{"type":"` + kind + `","input":"private payload"}`)}
			session := &responsesWebSocket{
				ctx: t.Context(), provider: newTestClient("http://unused.invalid", nil),
				clientMessages: messages, queuedCreate: []byte(`{"type":"response.create"}`),
			}
			var observed error
			var observedOrigin string
			exchange := &Exchange{ctx: t.Context(), session: session, ReadEnd: func(err error, origin string) { observed, observedOrigin = err, origin }}
			n, err := exchange.Read(make([]byte, 1))
			rejection, ok := errors.AsType[*CompatibilityError](err)
			if n != 0 || !ok || rejection.Code != "invalid_websocket_request" {
				t.Fatalf("request rejection changed: n=%d err=%v", n, err)
			}
			if !errors.Is(observed, err) || observedOrigin != "router_control" {
				t.Fatalf("control failure observation: %s, %v", observedOrigin, observed)
			}
		})
	}
}
