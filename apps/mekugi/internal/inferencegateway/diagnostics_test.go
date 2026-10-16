package inferencegateway

import (
	"errors"
	"io"
	"net/http"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/providers/opencode"
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
		DescribeError: func(err error) (string, string, bool) {
			if diagnostic, ok := errors.AsType[*testDiagnosticError](err); ok {
				return diagnostic.Code, diagnostic.Message, true
			}
			return "", "", false
		},
		Incompatible: IncompatibleRequest,
		ForwardError: func(err error) error { return err },
	}
}

func testTranslateProviderRequest(body []byte, service *opencode.Service) (*providerRequest, error) {
	return translateProviderRequest(body, service, testDiagnostics())
}

func testOpenCodePolicy(service *opencode.Service, model, format string) codexresponses.Policy {
	return openCodeTranslationPolicy(service, model, format, testDiagnostics())
}

func serverHTTPResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func newTestClient(baseURL string, client *http.Client) *Client {
	return NewClient(baseURL, client, testDiagnostics())
}
