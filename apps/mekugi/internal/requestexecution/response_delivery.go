// Source: apps/mekugi/internal/router/response_delivery.go:1:44@b9679c93.
package requestexecution

import (
	"fmt"
	"io"
	"net/http"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func CopyResponse(writer io.Writer, response *http.Response, streamResponse bool, transformer codexresponses.Transformer, hooks *ResponseHooks) (responseevents.TerminalState, error) {
	if streamResponse && hooks != nil && hooks.StreamDiagnostics != nil {
		transport := hooks.Transport
		if transport == "" {
			transport = "unknown"
		}
		hooks.StreamDiagnostics.ResponseStarted(response, transport)
		if id := response.Header.Get("X-Request-Id"); responseevents.SafeDiagnosticIdentity(id) {
			hooks.StreamDiagnostics.ProviderRequestID = id
		}
	}
	defer response.Body.Close()
	var reader io.Reader = response.Body
	if streamResponse && hooks != nil {
		reader = diagnosticStreamReader{reader: reader, diagnostics: hooks.StreamDiagnostics, receivedAt: &hooks.ReceivedAt}
	}
	terminalState, err := codexresponses.CopyBody(writer, reader, streamResponse, transformer, hooks)
	if streamResponse && hooks != nil {
		recordStreamCopyStop(hooks.StreamDiagnostics, err)
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
