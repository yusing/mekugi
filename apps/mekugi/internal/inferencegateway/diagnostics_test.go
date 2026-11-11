package inferencegateway

import (
	"io"
	"net/http"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

type testDiagnosticError struct{ Code, Message string }

func (e *testDiagnosticError) Error() string { return e.Message }

func testDiagnostics() codexresponses.Diagnostics {
	return codexresponses.Diagnostics{
		Error: func(code, summary, detail string) error {
			if detail != "" {
				summary = detail
			}
			return &testDiagnosticError{code, summary}
		},
	}
}

func serverHTTPResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func newTestClient(baseURL string, client *http.Client) *Client {
	return NewClient(baseURL, client, testDiagnostics())
}
