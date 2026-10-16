package router

import (
	"errors"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestStreamDiagnosticsRejectedWebSocketControl(t *testing.T) {
	for _, kind := range []string{"response.create", "response.steer"} {
		t.Run(kind, func(t *testing.T) {
			messages := make(chan webSocketMessage, 1)
			messages <- webSocketMessage{body: []byte(`{"type":"` + kind + `","input":"private payload"}`)}
			session := &responsesWebSocket{
				ctx: t.Context(), provider: newProviderClient("http://unused.invalid", nil),
				clientMessages: messages, queuedCreate: []byte(`{"type":"response.create"}`),
			}
			diagnostics := &streamDiagnostics{}
			exchange := &webSocketExchange{ctx: t.Context(), session: session, streamDiagnostics: diagnostics}
			n, err := exchange.Read(make([]byte, 1))
			rejection, ok := errors.AsType[*requestCompatibilityError](err)
			if n != 0 || !ok || rejection.code != "invalid_websocket_request" {
				t.Fatalf("request rejection changed: n=%d err=%v", n, err)
			}
			diagnostics.copyStopped(err)
			if diagnostics.ReadOrigin != "router_control" || diagnostics.EndReason != "router_control_invalid_websocket_request" || diagnostics.CopyStop != "read_error" {
				t.Fatalf("rejection diagnostics: %+v", diagnostics)
			}
			if strings.Contains(string(responses.MustMarshalJSON(diagnostics.snapshot())), "private") {
				t.Fatal("control payload leaked into diagnostics")
			}
		})
	}
}
