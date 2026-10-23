package launch

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
)

func TestModelsHandlerPreservesNativeCatalogWithGrokEnabled(t *testing.T) {
	body := `{"models":[{"slug":"gpt-5.6-sol","multi_agent_version":"v2"}]}`
	client := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Etag": []string{`"native-etag"`}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	provider := newTestProviderClient(testProviderBaseURL, client)
	provider.Grok = &inferencegateway.ThirdPartyClient{Diagnostics: requestexecution.ProviderDiagnostics()}
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header = codexAuthHeaders()
	response := httptest.NewRecorder()
	inferencegateway.ModelsHandler(provider, false, (*requestexecution.Failures)(nil).ReportCatalog, nil)(response, request)
	if response.Code != http.StatusOK || response.Body.String() != body || response.Header().Get("ETag") != `"native-etag"` {
		t.Fatalf("native catalog was rewritten: status=%d body=%s etag=%s", response.Code, response.Body.String(), response.Header().Get("ETag"))
	}
}
