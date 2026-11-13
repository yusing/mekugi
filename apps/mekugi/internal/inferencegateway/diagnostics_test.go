package inferencegateway

import (
	"net/http"

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

func newTestClient(baseURL string, client *http.Client) *Client {
	return NewClient(baseURL, client, testDiagnostics())
}
