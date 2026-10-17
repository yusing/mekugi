package router

import (
	"io"
	"net/http"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func copySSETransformed(writer io.Writer, reader io.Reader, transformer codexresponses.Transformer, hooks *requestexecution.ResponseHooks) (responses.TerminalState, error) {
	return requestexecution.CopyResponse(writer, &http.Response{Body: io.NopCloser(reader), Header: http.Header{}}, true, transformer, hooks)
}
