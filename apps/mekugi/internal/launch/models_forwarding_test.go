package launch

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestModelsHandlerPreservesEachCatalog(t *testing.T) {
	const catalog = `{"models":[{"slug":"gpt-6-astra","tool_mode":"code_mode","future":42}],"extra":true}`
	calls := 0
	client := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return serverHTTPResponse(`{"models":[null]}`), nil
		}
		response := serverHTTPResponse(catalog)
		response.Header.Set("ETag", "account-version")
		return response, nil
	})}
	provider := newTestProviderClient(testProviderBaseURL, client)
	handler := inferencegateway.ModelsHandler(provider, nil, nil)
	for request := range 3 {
		if request == 2 {
			// Native configuration owns tool mode for custom catalogs too.
			provider.Native = &inferencegateway.NativeProvider{ID: "custom", CatalogURL: "http://custom.invalid/models", HTTPClient: client}
		}
		recorder := httptest.NewRecorder()
		handler(recorder, catalogRequest())
		if request == 0 {
			if recorder.Code != http.StatusOK || recorder.Body.String() != `{"models":[null]}` {
				t.Fatalf("catalog changed before native validation: %d %s", recorder.Code, recorder.Body)
			}
			continue
		}
		if recorder.Code != http.StatusOK || recorder.Body.String() != catalog ||
			recorder.Header().Get("ETag") != "account-version" || calls != request+1 {
			t.Fatalf("forwarded catalog = %d %s; calls=%d", recorder.Code, recorder.Body, calls)
		}
	}
}

func catalogRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.157.1", nil)
	r.Header = codexAuthHeaders()
	r.Header.Set(codexresponses.RoutingSessionHeader, "session-one")
	return r
}
