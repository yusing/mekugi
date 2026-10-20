package executionobservation

import (
	"bytes"

	"context"

	"encoding/json"

	"errors"

	"io"

	"net/http"

	"strings"

	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

type serverForwardResult struct {
	response *http.Response
	err      error
}

type serverFakeProvider struct {
	results           []serverForwardResult
	forwarded         [][]byte
	forwardedHeaders  []http.Header
	forwardedCacheKey []string
}

func (f *serverFakeProvider) ForwardExecution(_, _ context.Context, body []byte, headers http.Header, cacheKey string) (*inferencegateway.Response, error) {
	f.forwardedHeaders = append(f.forwardedHeaders, headers.Clone())
	f.forwardedCacheKey = append(f.forwardedCacheKey, cacheKey)
	return testGatewayResponse(f.next(body))
}

func (f *serverFakeProvider) next(body []byte) (*http.Response, error) {
	f.forwarded = append(f.forwarded, bytes.Clone(body))
	if len(f.results) == 0 {
		return nil, errors.New("unexpected upstream forward")
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result.response, result.err
}

func serverHTTPResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func serverCompactionMetadataHeaders(t *testing.T) http.Header {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"request_kind": "compaction",
		"turn_id":      "turn-1",
		"compaction": map[string]any{
			"trigger": "auto", "reason": "context_limit", "implementation": "responses",
			"phase": "standalone_turn", "strategy": "memento",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set(codexresponses.TurnMetadataHeader, string(encoded))
	return headers
}
