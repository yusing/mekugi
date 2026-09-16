package router

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestDashboardUsesCaptureMetricsOnTheExistingListener(t *testing.T) {
	recorder := httptest.NewRecorder()
	serveDashboard(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if csp := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "font-src 'none'") {
		t.Fatalf("CSP = %q", csp)
	}
	body := recorder.Body.String()
	for _, required := range []string{
		"Token<br>Telemetry", "Skip to content", `role="tablist"`, `data-view="overview"`,
		`id="status-pill"`, `class="cards"`, "prefers-reduced-motion",
		"fetch('/api/metrics'", "mekugi.capture.metrics.v4", "Provider usage",
		"Client steering requests", "Provider steering requests", "Provider steering events", "Client steering events",
		"Translation delivery, not host application", "Unclassified", "data.mekugi.unclassified",
		"Transport", "Protocol representation", "Hashline edits", "Hashline edit diagnostics",
		"Provider response evidence", "provider_response", "Provider request ID", "Explicit cached tokens", "Cache-prefix diagnostics", "cache_diagnostics", "Post-replay prefix", "Outgoing route key", "Turn-state forwarding", "turn_state_forwarding", "Capture health", "Tool transport", "Recent exchanges", "Provider attempts",
		"Provider tool calls", "Delivered tool calls", "Usage-bearing attempts", "Provider input tokens",
		"Delivered input tokens", "Input bytes", "Item bytes", "response_complete", "Semantic output",
		"provider_attempt_outputs", "client_final_output", "Provider-attempt model outputs",
		"Client model outputs", "<th>Client model output</th>", "<th>Provider model output</th>",
		"Complete model-origin output arrays", "excludes router-generated commentary",
		"Exact wire bytes; decoded-content token estimates", "Delivery token expansion", "CTP assistant-text tokens saved", "native_request", "Raw Codex request", "Provider request (native/CTP)",
		`value==null?'unavailable':payload(value)`, "measuredPayload(attempt.native_request)",
		"thread_id", "uncached_input_tokens", "id=\"newer\"", "id=\"older\"",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("dashboard is missing %q", required)
		}
	}
	for _, forbidden := range []string{"EventSource(", "fonts.googleapis.com", "fonts.gstatic.com", ".innerHTML"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("dashboard contains obsolete or unsafe fragment %q", forbidden)
		}
	}
}

func TestDashboardRejectsUnrelatedPaths(t *testing.T) {
	recorder := httptest.NewRecorder()
	serveDashboard(recorder, httptest.NewRequest(http.MethodGet, "/future", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestDashboardPollingRunsSerially(t *testing.T) {
	t.Parallel()
	command := exec.CommandContext(t.Context(), "node", "--test", "dashboard.test.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("dashboard polling: %v\n%s", err, output)
	}
}
