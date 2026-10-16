package router

import (
	"fmt"
	"io"
	"net/http"

	"github.com/yusing/mekugi/providers/openai"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func copyUpstreamBodyTransformed(writer io.Writer, response *http.Response, streamResponse bool, transformer codexresponses.Transformer, hooks *responseHooks) (responseevents.TerminalState, error) {
	if streamResponse && hooks != nil && hooks.streamDiagnostics != nil {
		fallback := "unknown"
		switch response.Body.(type) {
		case *openai.WebSocketBody, *webSocketExchange:
			fallback = "websocket"
		case *grokResponseBody:
			fallback = "chat_response_bridge"
		}
		hooks.streamDiagnostics.ResponseStarted(response, fallback)
		if id := response.Header.Get("X-Request-Id"); responseevents.SafeDiagnosticIdentity(id) {
			hooks.streamDiagnostics.ProviderRequestID = id
		}
	}
	defer response.Body.Close()
	var reader io.Reader = response.Body
	if streamResponse && hooks != nil {
		reader = diagnosticStreamReader{reader: reader, diagnostics: hooks.streamDiagnostics, receivedAt: &hooks.receivedAt}
	}
	terminalState, err := codexresponses.CopyBody(writer, reader, streamResponse, transformer, hooks)
	if streamResponse && hooks != nil {
		recordStreamCopyStop(hooks.streamDiagnostics, err)
	}
	// The response bridge emits a failed terminal before closing its local pipe.
	// SSE copying stops at that terminal, so retrieve its producer error without
	// draining a provider stream or waiting after a downstream write failure.
	if err == nil && terminalState == responseevents.TerminalFailed {
		if body, ok := response.Body.(interface{ TerminalError() error }); ok {
			err = body.TerminalError()
		}
	}
	if err != nil {
		return terminalState, fmt.Errorf("copy upstream response: %w", err)
	}
	return terminalState, nil
}
