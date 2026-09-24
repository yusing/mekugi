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
	for _, forbidden := range []string{"EventSource(", "fonts.googleapis.com", "fonts.gstatic.com", ".innerHTML", "data.mekugi"} {
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
