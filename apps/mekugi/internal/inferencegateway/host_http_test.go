package inferencegateway

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yusing/mekugi/providers/openai"
)

func hostHTTPHeaders() http.Header {
	return http.Header{"Authorization": {"Bearer fixture"}, openai.AccountIDHeader: {"account"}}
}

func TestForwardExecutionHTTPDoesNotProbeWebSocket(t *testing.T) {
	var calls, upgrades atomic.Int32
	const payload = `{"model":"gpt-test","input":[],"stream":true}`
	const stream = "data: {\"type\":\"response.completed\"}\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Upgrade") != "" {
			upgrades.Add(1)
			http.Error(w, "WebSocket unavailable", http.StatusUpgradeRequired)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != payload || r.Method != http.MethodPost || r.URL.Path != "/responses" {
			t.Errorf("HTTP request: %s %s body=%q err=%v", r.Method, r.URL, body, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer upstream.Close()
	client := newTestClient(upstream.URL, upstream.Client())
	response, err := client.ForwardExecution(t.Context(), t.Context(), []byte(payload), hostHTTPHeaders(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != stream || response.StatusCode != http.StatusOK || response.Transport != "http" || calls.Load() != 1 || upgrades.Load() != 0 {
		t.Fatalf("HTTP forwarding: transport=%q status=%d body=%q calls=%d upgrades=%d err=%v", response.Transport, response.StatusCode, body, calls.Load(), upgrades.Load(), err)
	}
}

func TestModelsHandlerHTTPRefreshesSameIdentity(t *testing.T) {
	var calls atomic.Int32
	const failure = "catalog temporarily unavailable\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := calls.Add(1)
		if r.URL.Path != "/models" || r.URL.RawQuery != "client_version=fixture" || r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get(openai.AccountIDHeader) != "account" {
			t.Errorf("catalog request: %s headers=%v", r.URL, r.Header)
		}
		w.Header().Set("ETag", fmt.Sprintf(`"version-%d"`, attempt))
		if attempt == 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, failure)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"models":[{"slug":"model-%d","tool_mode":"code_mode","future":42}],"extra":true}`, attempt)
	}))
	defer upstream.Close()
	var reports []CatalogFailure
	handler := ModelsHandler(newTestClient(upstream.URL, upstream.Client()), true, func(_ *http.Request, failure CatalogFailure) { reports = append(reports, failure) }, nil)
	for attempt := 1; attempt <= 3; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=fixture", nil)
		request.Header = hostHTTPHeaders()
		writer := httptest.NewRecorder()
		handler(writer, request)
		if calls.Load() != int32(attempt) || writer.Header().Get("ETag") != fmt.Sprintf(`"version-%d"`, attempt) {
			t.Fatalf("catalog attempt %d: calls=%d ETag=%q", attempt, calls.Load(), writer.Header().Get("ETag"))
		}
		if attempt == 3 {
			if writer.Code != http.StatusServiceUnavailable || writer.Body.String() != failure || len(reports) != 1 || reports[0].UpstreamStatus != http.StatusServiceUnavailable {
				t.Fatalf("later upstream failure: status=%d body=%q reports=%+v", writer.Code, writer.Body.String(), reports)
			}
			continue
		}
		var catalog struct {
			Models []struct {
				Slug   string `json:"slug"`
				Mode   string `json:"tool_mode"`
				Future int    `json:"future"`
			} `json:"models"`
			Extra bool `json:"extra"`
		}
		if err := json.Unmarshal(writer.Body.Bytes(), &catalog); err != nil {
			t.Fatal(err)
		}
		if writer.Code != http.StatusOK || len(catalog.Models) != 1 || catalog.Models[0].Slug != fmt.Sprintf("model-%d", attempt) || catalog.Models[0].Mode != "code_mode_only" || catalog.Models[0].Future != 42 || !catalog.Extra {
			t.Fatalf("catalog projection: status=%d body=%s", writer.Code, strings.TrimSpace(writer.Body.String()))
		}
	}
}
