package launch

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestModelsHandlerValidatesAndProjectsEachCatalog(t *testing.T) {
	const catalog = `{"models":[{"slug":"gpt-6-astra","tool_mode":"code_mode","future":42}],"extra":true}`
	calls := 0
	provider := newTestProviderClient(testProviderBaseURL, &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return serverHTTPResponse(`{"models":[null]}`), nil
		}
		response := serverHTTPResponse(catalog)
		response.Header.Set("ETag", "account-version")
		return response, nil
	})})
	handler := inferencegateway.ModelsHandler(provider, true, nil)
	for request := range 3 {
		recorder := httptest.NewRecorder()
		handler(recorder, catalogRequest())
		if request == 0 {
			if recorder.Code != http.StatusBadGateway {
				t.Fatalf("malformed catalog returned %d", recorder.Code)
			}
			continue
		}
		var got struct {
			Models []struct {
				Slug   string `json:"slug"`
				Mode   string `json:"tool_mode"`
				Future int    `json:"future"`
			} `json:"models"`
			Extra bool `json:"extra"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK || len(got.Models) != 1 || got.Models[0].Slug != "gpt-6-astra" ||
			got.Models[0].Mode != "code_mode_only" || got.Models[0].Future != 42 || !got.Extra ||
			recorder.Header().Get("ETag") != "account-version" || calls != request+1 {
			t.Fatalf("projected catalog = %d %s; calls=%d", recorder.Code, recorder.Body, calls)
		}
	}
}

func catalogRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.157.1", nil)
	r.Header = codexAuthHeaders()
	r.Header.Set(codexresponses.RoutingSessionHeader, "session-one")
	return r
}
