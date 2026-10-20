package executionobservation

import (
	"io"
	"net/http"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

const threadSpawnSubagentKind = "thread_spawn"

func copySSETransformed(writer io.Writer, reader io.Reader, transformer codexresponses.Transformer, hooks *requestexecution.ResponseHooks) (responses.TerminalState, error) {
	return requestexecution.CopyResponse(writer, &http.Response{Body: io.NopCloser(reader), Header: http.Header{}}, true, transformer, hooks)
}

type webSocketCountingTransform struct{ jsonCalls, streamCalls, finishes int }

func (c *webSocketCountingTransform) TransformJSON(payload []byte) ([]byte, error) {
	c.jsonCalls++
	return payload, nil
}
func (c *webSocketCountingTransform) TransformSSE(payload []byte) ([][]byte, error) {
	c.streamCalls++
	return [][]byte{payload}, nil
}
func (c *webSocketCountingTransform) Finish(bool) error { c.finishes++; return nil }
