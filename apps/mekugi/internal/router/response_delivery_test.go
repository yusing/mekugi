package router

import (
	"io"
	"net/http"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func copySSETransformed(writer io.Writer, reader io.Reader, transformer codexresponses.Transformer, hooks *responseHooks) (responses.TerminalState, error) {
	return copyUpstreamBodyTransformed(writer, &http.Response{Body: io.NopCloser(reader), Header: http.Header{}}, true, transformer, hooks)
}
