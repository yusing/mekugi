package router

import (
	"encoding/base64"

	terminalsession "github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/persistence"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func TestSessionMetricsWrappedDirectoryCopy(t *testing.T) {
	const path = "/tmp/mekugi debug/a-long-session-directory/mekugi-debug-3106556733"
	for _, width := range []int{35, 100} {
		u, _ := newAppServerTestUI()
		u.replayDebugDirectory = path
		u.showSessionMetrics()
		screen := vt.NewEmulator(width, 24)
		defer screen.Close()
		if err := u.testPaint(screen, width, 24); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { u.shell.diffScreen.Close() })
		x1, y1, x2, y2 := -1, -1, -1, -1
		for y, row := range strings.Split(screen.String(), "\n") {
			if before, _, ok := strings.Cut(row, "/tmp/"); ok {
				x1, y1 = ansi.StringWidth(before), y
			}
			if before, _, ok := strings.Cut(row, "3106556733"); ok {
				x2, y2 = ansi.StringWidth(before)+len("3106556733")-1, y
			}
		}
		if x1 < 0 || y2 <= y1 {
			t.Fatalf("wrapped directory missing at width %d: %s", width, screen.String())
		}
		if width == 35 {
			x1, y1, x2, y2 = x2, y2, x1, y1
		}
		selectionTestDrag(t, u.shell, x1, y1, x2, y2)
		u.shell.selectionAction('c')
		if want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(path)) + "\x07"; u.shell.frame.Clipboard != want {
			t.Fatalf("wrapped directory copy = %q, want %q", u.shell.frame.Clipboard, want)
		}
	}
}

// Decode the public snapshot boundary rather than naming capture-owned types.
func sessionMetricsFixture(t *testing.T) transportcapture.MetricsSnapshot {
	t.Helper()
	var snapshot transportcapture.MetricsSnapshot
	err := json.Unmarshal([]byte(`{
  "schema":"mekugi.capture.metrics.v7","mode":"mekugi",
  "requests":{"logical":2,"provider_attempts":3,"retries":1,"completed":2,"failed":0},
  "usage":{"output_throughput":{"output_tokens":12,"duration_ns":10000000000,"measured_requests":2},"input_tokens":120,"cached_input_tokens":0,"uncached_input_tokens":120,"output_tokens":12,"reasoning_tokens":4,"provider_attempts":2,"complete_attempts":1,"incomplete_attempts":1,"missing_attempts":1},
  "cache":{"provider_cache_rate":0},
  "capture":{"records":5,"dropped_exchange_details":7},
  "transport":{
    "client_requests":{"bytes":100},"provider_attempt_requests":{"bytes":250},
    "provider_responses":{"bytes":900},"client_responses":{"bytes":950},
    "client_control_requests":{"bytes":11},"client_control_responses":{"bytes":22},
    "provider_control_requests":{"bytes":33},"provider_control_responses":{"bytes":44}},
  "exchanges":[
    {"sequence":8,"thread_id":"main","model":"gpt-6-sol","status":"completed","duration_ms":1200,"status_code":200,"response_complete":true,
      "provider_attempts":[{"attempt":1,"transport":"http","status":"failed","provider_response":{"cached_tokens_state":"missing"}}]},
    {"sequence":9,"thread_id":"side","model":"gpt-6-sol","status":"completed","duration_ms":2500,"status_code":200,"response_complete":true,
      "usage":{"input_tokens":120,"cached_input_tokens":0,"uncached_input_tokens":120,"output_tokens":12,"reasoning_tokens":4},
      "provider_attempts":[
        {"attempt":1,"transport":"http","status":"http_error","status_code":429,"duration_ms":200,"response_complete":true,"capture_error":"missing_projected_request"},
        {"attempt":2,"transport":"websocket","status":"completed","response_complete":true,"duration_ms":1800,"status_code":101,
         "usage":{"input_tokens":120,"cached_input_tokens":0,"uncached_input_tokens":120,"output_tokens":12,"complete_attempts":1},
         "provider_response":{"model":"gpt-6-sol","service_tier":"priority","request_id":"req-2","cached_tokens_state":"present","cached_tokens":0}}]}]}`), &snapshot, json.RejectUnknownMembers(true))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func sessionMetricField(t *testing.T, u *appServerUI, group, label, want string) {
	t.Helper()
	u.settlePresentation()
	for _, field := range u.mainView.Panel.Fields {
		if field.Group == group && field.Label == label {
			if field.Value != want {
				t.Fatalf("%s/%s = %q, want %q", group, label, field.Value, want)
			}
			return
		}
	}
	t.Fatalf("missing %s/%s", group, label)
}

func TestAppServerSessionMetricsLocalNavigation(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Turn, u.sessionPresentation().Status.Text = "active", "Working"
	appServerTestKeys(t, u, "/session\r")
	if u.statusPanel == nil || u.statusPanel.metrics == nil || u.Text != "" {
		t.Fatal("session command did not open a local dialog")
	}
	u.statusPanel.metrics.snapshot = sessionMetricsFixture(t)
	u.settlePresentation()
	sessionMetricField(t, u, "Requests", "Attempts", "3")
	sessionMetricField(t, u, "Usage", "Average output", "1.2 tok/s")
	sessionMetricField(t, u, "Usage", "Measured duration", "10.000 s")
	sessionMetricField(t, u, "Cache", "Provider rate", "0.0%")
	u.statusPanelFrame(80, 12)
	u.statusPanelKey("\x1b[B")
	if u.mainView.Panel.Top == 0 {
		t.Fatal("scroll did not move")
	}
	u.statusPanelKey("\t")
	if u.mainView.Panel.Tab != 1 || u.mainView.Panel.Top != 0 {
		t.Fatal("tab switch did not reset scroll")
	}
	sessionMetricField(t, u, "Transport", "Provider requests", "250 bytes")
	for _, tc := range []struct{ label, want string }{
		{"Client control requests", "11 bytes"},
		{"Client control responses", "22 bytes"},
		{"Provider control requests", "33 bytes"},
		{"Provider control responses", "44 bytes"},
	} {
		sessionMetricField(t, u, "Transport", tc.label, tc.want)
	}
	u.statusPanelKey("\x1b[C")
	sessionMetricField(t, u, "Exchange", "Sequence", "9")
	sessionMetricField(t, u, "Provider attempt 1", "Usage", "unavailable")
	sessionMetricField(t, u, "Provider attempt 2", "Transport", "WebSocket")
	sessionMetricField(t, u, "Provider attempt 2", "Explicit cached", "0")
	u.statusPanelKey("[")
	sessionMetricField(t, u, "Exchange", "Sequence", "8")
	sessionMetricField(t, u, "Exchange", "Usage", "unavailable")
	sessionMetricField(t, u, "Provider attempt 1", "Cached telemetry", "missing")
	u.statusPanelKey("[") // Clamp at oldest.
	sessionMetricField(t, u, "Exchange", "Sequence", "8")
	u.statusPanelKey("]")
	if u.mainView.Panel.Sequence != 0 {
		t.Fatal("newest exchange must follow subsequent completions")
	}
	u.statusPanelKey("\x1b[Z")
	if u.mainView.Panel.Tab != 1 {
		t.Fatal("reverse tab did not select transport")
	}
	u.statusPanelKey("r")
	u.statusPanelKey("\x1b")
	if u.statusPanel != nil || wire.Len() != 0 || len(u.source.Entries) != 0 || u.Turn != "active" || u.sessionPresentation().Status.Text != "Working" {
		t.Fatalf("local dialog disturbed active session: wire=%q turn=%q status=%q", wire.String(), u.Turn, u.sessionPresentation().Status.Text)
	}
}

func TestAppServerSessionMetricsSelectionSurvivesRetention(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.showSessionMetrics()
	u.statusPanel.metrics.snapshot = sessionMetricsFixture(t)
	u.statusPanelKey("\x1b[C")
	u.statusPanelKey("\x1b[C")
	u.statusPanelKey("[")
	sessionMetricField(t, u, "Exchange", "Sequence", "8")
	m := u.statusPanel.metrics
	m.snapshot.Exchanges = m.snapshot.Exchanges[1:]
	sessionMetricField(t, u, "Exchange", "Sequence", "9")
	if u.mainView.Panel.Sequence != 0 {
		t.Fatal("reclaimed selection must follow newest")
	}
	m.snapshot.Exchanges = nil
	sessionMetricField(t, u, "Exchanges", "", "No completed exchanges yet. Active requests appear when they settle.")
}

func TestAppServerSessionMetricsRefreshAndBounds(t *testing.T) {
	capture, err := new(transportcapture.Service).Open(transportcapture.Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { capture.Close() })
	u, _ := newAppServerTestUI()
	now := time.Unix(100, 0)
	u.clock = func() time.Time { return now }
	u.sessionCapture = capture
	u.showSessionMetrics()
	handler := capture.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r","object":"response","status":"completed","output":[]}`))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test","input":[]}`)))
	now = now.Add(500 * time.Millisecond)
	u.refreshSessionMetrics(false)
	sessionMetricField(t, u, "Requests", "Logical", "0")
	now = now.Add(500 * time.Millisecond)
	u.refreshSessionMetrics(false)
	sessionMetricField(t, u, "Requests", "Logical", "1")
	for _, theme := range []diffrender.Theme{diffrender.DarkTheme, diffrender.LightTheme} {
		u.view.Painter.Theme = theme
		for tab := range 3 {
			u.mainView.Panel.Tab = tab
			u.settlePresentation()
			for _, size := range [][2]int{{90, 25}, {30, 15}, {12, 8}, {1, 1}, {0, 0}} {
				for _, row := range u.statusPanelFrame(size[0], size[1]) {
					if ansi.StringWidth(row) > size[0] {
						t.Fatalf("tab %d size %v overflow: %q", tab, size, row)
					}
				}
			}
		}
	}
	u.statusPanelKey("\x1b")
	u.refreshSessionMetrics(true)
	if u.statusPanel != nil {
		t.Fatal("late refresh revived a closed dialog")
	}
}

func TestUISnapshotAppServerSessionMetrics(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		tab, width, height, top int
		empty, missing          bool
		throughput              string
		debug                   string
	}{
		{name: "overview", width: 90, height: 65},
		{name: "deduplication", width: 90, height: 25},
		{name: "deduplication-narrow", width: 35, height: 22},
		{name: "storage", width: 90, height: 24},
		{name: "storage-narrow", width: 35, height: 18},
		{name: "throughput-narrow", width: 36, height: 22, top: 18},
		{name: "throughput-absent", width: 90, height: 25, top: 14, throughput: "absent"},
		{name: "throughput-zero", width: 90, height: 25, top: 14, throughput: "zero"},
		{name: "transport", tab: 1, width: 90, height: 55},
		{name: "exchanges", tab: 2, width: 90, height: 60},
		{name: "exchanges-scrolled", tab: 2, width: 90, height: 25, top: 27},
		{name: "provider-tier", tab: 2, width: 90, height: 25, top: 42},
		{name: "narrow", tab: 1, width: 30, height: 18},
		{name: "empty", tab: 2, width: 80, height: 14, empty: true},
		{name: "missing", width: 80, height: 14, missing: true},
		{name: "debug", width: 90, height: 18, debug: "/workspace/.mekugi/debug/run-42"},
		{name: "debug-external", width: 90, height: 18, debug: "/tmp/mekugi debug/run-42"},
		{name: "debug-narrow", width: 30, height: 18, debug: "/tmp/mekugi debug/run-42"},
		{name: "debug-missing", width: 80, height: 14, missing: true, debug: "/tmp/mekugi-debug/run-42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.view.Painter.Theme = diffrender.DarkTheme
			u.cwd = "/workspace"
			u.replayDebugDirectory = tc.debug
			u.showSessionMetrics()
			u.settlePresentation()
			m := u.statusPanel.metrics
			u.mainView.Panel.Tab = tc.tab
			if !tc.missing {
				m.snapshot = sessionMetricsFixture(t)
			}
			if strings.HasPrefix(tc.name, "deduplication") {
				m.snapshot.OutputDeduplication = transportcapture.OutputDeduplicationMetrics{Projections: 3, Occurrences: 12, SavedTokens: 1234}
			}
			if strings.HasPrefix(tc.name, "storage") {
				m.snapshot.StorageWrites = &persistence.Snapshot{Bytes: 12345, Scope: new(persistence.Counter).Snapshot().Scope}
			}
			if tc.throughput == "absent" {
				m.snapshot.Usage.OutputThroughput = inference.OutputThroughput{}
			}
			if tc.throughput == "zero" {
				m.snapshot.Usage.OutputThroughput.OutputTokens = 0
			}
			if tc.empty {
				m.snapshot.Exchanges = nil
			}
			u.settlePresentation()
			u.mainView.Panel.Top = tc.top
			assertNativeUISnapshot(t, "native-session-"+tc.name, u.statusPanelFrame(tc.width, tc.height))
		})
	}
}

func TestUISnapshotNativeSessionShortcuts(t *testing.T) {
	for _, width := range []int{40, 140} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			assertNativeUISnapshot(t, fmt.Sprintf("native-session-shortcuts-%d", width), terminalsession.Keybindings(width, 40))
		})
	}
}
