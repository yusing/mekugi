package inferencegateway

// Source: apps/mekugi/internal/router/server.go:569:613@[e5c6fadc] HTTP admission and delivery.
import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func ResponsesHandler(lifecycle context.Context, responseStartTimeout time.Duration, execute func(context.Context, context.Context, codexresponses.Request, http.Header, io.Writer) error) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		trackedWriter := &ResponseWriter{ResponseWriter: writer}
		body, status, err := readCodexHTTPBody(request)
		if err != nil {
			http.Error(trackedWriter, err.Error(), status)
			return
		}
		if len(bytes.TrimSpace(body)) == 0 {
			http.Error(trackedWriter, "responses request body is empty", http.StatusBadRequest)
			return
		}
		parsedRequest, err := codexresponses.ParseRequest(body)
		if err != nil {
			http.Error(trackedWriter, err.Error(), http.StatusBadRequest)
			return
		}
		startCtx, executionCtx, cancelRequest := RequestContexts(request.Context(), lifecycle, responseStartTimeout)
		defer cancelRequest()
		if err := execute(startCtx, executionCtx, parsedRequest, request.Header, trackedWriter); err != nil {
			writeRequestError(trackedWriter, err)
		}
	}
}

func writeRequestError(writer *ResponseWriter, requestErr error) {
	if !writer.ResponseStarted() {
		status := http.StatusBadGateway
		if _, ok := errors.AsType[*CompatibilityError](requestErr); ok {
			status = http.StatusBadRequest
		}
		if rejection, ok := errors.AsType[*ProviderHTTPError](requestErr); ok {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(rejection.Status)
			_, _ = writer.Write(responses.MustMarshalJSON(map[string]any{"error": map[string]string{
				"type": "provider_error", "message": rejection.Error(),
			}}))
			return
		}
		http.Error(writer, requestErr.Error(), status)
	}
}
