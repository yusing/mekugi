package router

import (
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/persistence"
)

func TestSessionMetricsWrappedDirectoryCopy(t *testing.T) {
	const path = "/tmp/mekugi debug/a-long-session-directory/mekugi-debug-3106556733"
	for _, width := range []int{35, 100} {
		u, _ := newAppServerTestUI()
		u.replayDebugDirectory = path
		u.showSessionMetrics()
		screen := vt.NewEmulator(width, 24)
		defer screen.Close()
		if err := u.paint(screen, width, 24); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
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
		if want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(path)) + "\x07"; u.shell.clipboard != want {
			t.Fatalf("wrapped directory copy = %q, want %q", u.shell.clipboard, want)
		}
	}
}

// Decode the public snapshot boundary rather than naming capture-owned types.
func sessionMetricsFixture(t *testing.T) capturer.MetricsSnapshot {
	t.Helper()
	var snapshot capturer.MetricsSnapshot
	err := json.Unmarshal([]byte(`{
  "schema":"mekugi.capture.metrics.v7","mode":"mekugi",
  "requests":{"logical":2,"provider_attempts":3,"retries":1,"completed":2,"failed":0},
  "usage":{"output_throughput":{"output_tokens":12,"duration_ns":10000000000,"measured_requests":2},"input_tokens":120,"cached_input_tokens":0,"uncached_input_tokens":120,"output_tokens":12,"reasoning_tokens":4,"provider_attempts":2,"complete_attempts":1,"incomplete_attempts":1,"missing_attempts":1},
  "cache":{"provider_cache_rate":0},
  "capture":{"records":5,"dropped_exchange_details":7},
  "transport":{
    "client_requests":{"bytes":100,"tokens":10},"provider_attempt_requests":{"bytes":250,"tokens":25},
    "provider_responses":{"bytes":900,"tokens":90},"client_responses":{"bytes":950,"tokens":95},
    "client_control_requests":{"bytes":11,"tokens":1},"client_control_responses":{"bytes":22,"tokens":2},
    "provider_control_requests":{"bytes":33,"tokens":3},"provider_control_responses":{"bytes":44,"tokens":4}},
  "semantic":{"provider_attempt_outputs":{"bytes":80,"tokens":8},"client_outputs":{"bytes":85,"tokens":9}},
  "provider_tools":{"exec_command":{"calls":2,"input_bytes":12,"input_tokens":3,"item_bytes":30,"item_tokens":7}},
  "delivered_tools":{"exec_command":{"calls":1,"input_bytes":18,"input_tokens":4,"item_bytes":40,"item_tokens":9}},
  "exchanges":[
    {"sequence":8,"thread_id":"main","model":"gpt-6-sol","status":"completed","duration_ms":1200,"status_code":200,"response_complete":true,
      "provider_attempts":[{"attempt":1,"transport":"http","status":"failed","provider_response":{"cached_tokens_state":"missing"}}]},
    {"sequence":9,"thread_id":"side","model":"gpt-6-sol","status":"completed","duration_ms":2500,"status_code":200,"response_complete":true,
      "usage":{"input_tokens":120,"cached_input_tokens":0,"uncached_input_tokens":120,"output_tokens":12,"reasoning_tokens":4},
      "provider_attempts":[
        {"attempt":1,"transport":"http","status":"http_error","status_code":429,"duration_ms":200,"response_complete":true,"capture_error":"missing_projected_request"},
        {"attempt":2,"transport":"websocket","status":"completed","response_complete":true,"duration_ms":1800,"status_code":101,
         "usage":{"input_tokens":120,"cached_input_tokens":0,"uncached_input_tokens":120,"output_tokens":12,"complete_attempts":1},
         "provider_response":{"model":"gpt-6-sol","service_tier":"priority","request_id":"req-2","cached_tokens_state":"present","cached_tokens":0},
         "tools":[{"name":"exec_command","call_id":"call-1","input_bytes":12,"input_tokens":3,"item_bytes":30,"item_tokens":7}]}],
      "delivered_tools":[{"name":"exec_command","call_id":"call-1","input_bytes":18,"input_tokens":4,"item_bytes":40,"item_tokens":9}]}]}`), &snapshot, json.RejectUnknownMembers(true))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func sessionMetricField(t *testing.T, u *appServerUI, group, label, want string) {
	t.Helper()
	for _, field := range u.statusPanel.fields {
		if field.group == group && field.label == label {
			if field.value != want {
				t.Fatalf("%s/%s = %q, want %q", group, label, field.value, want)
			}
			return
		}
	}
	t.Fatalf("missing %s/%s", group, label)
}

func TestAppServerSessionMetricsLocalNavigation(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.turn, u.status = "active", "Working"
	appServerTestKeys(t, u, "/session\r")
	if u.statusPanel == nil || u.statusPanel.metrics == nil || u.draft != "" {
		t.Fatal("session command did not open a local dialog")
	}
	u.statusPanel.metrics.snapshot = sessionMetricsFixture(t)
	u.renderSessionMetrics()
	sessionMetricField(t, u, "Requests", "Attempts", "3")
	sessionMetricField(t, u, "Usage", "Average output", "1.2 tok/s")
	sessionMetricField(t, u, "Usage", "Measured duration", "10.000 s")
	sessionMetricField(t, u, "Cache", "Provider rate", "0.0%")
	u.statusPanelFrame(80, 12)
	u.statusPanelKey("\x1b[B")
	if u.statusPanel.top == 0 {
		t.Fatal("scroll did not move")
	}
	u.statusPanelKey("\t")
	if u.statusPanel.metrics.tab != 1 || u.statusPanel.top != 0 {
		t.Fatal("tab switch did not reset scroll")
	}
	sessionMetricField(t, u, "Transport", "Provider requests", "250 bytes · 25 estimated tokens")
	for _, tc := range []struct{ label, want string }{
		{"Client control requests", "11 bytes · 1 estimated tokens"},
		{"Client control responses", "22 bytes · 2 estimated tokens"},
		{"Provider control requests", "33 bytes · 3 estimated tokens"},
		{"Provider control responses", "44 bytes · 4 estimated tokens"},
	} {
		sessionMetricField(t, u, "Transport", tc.label, tc.want)
	}
	sessionMetricField(t, u, "Provider tools", "exec_command", "2 calls")
	sessionMetricField(t, u, "Delivered tools", "exec_command", "1 calls")
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
	if u.statusPanel.metrics.sequence != 0 {
		t.Fatal("newest exchange must follow subsequent completions")
	}
	u.statusPanelKey("\x1b[Z")
	if u.statusPanel.metrics.tab != 1 {
		t.Fatal("reverse tab did not select transport")
	}
	u.statusPanelKey("r")
	u.statusPanelKey("\x1b")
	if u.statusPanel != nil || wire.Len() != 0 || len(u.view.entries) != 0 || u.turn != "active" || u.status != "Working" {
		t.Fatalf("local dialog disturbed active session: wire=%q turn=%q status=%q", wire.String(), u.turn, u.status)
	}
}

func TestAppServerSessionMetricsSelectionSurvivesRetention(t *testing.T) {
	m := sessionMetricsPanel{snapshot: sessionMetricsFixture(t), sequence: 8}
	if m.exchangeIndex() != 0 {
		t.Fatal("selected exchange was not found")
	}
	m.snapshot.Exchanges = m.snapshot.Exchanges[1:]
	if m.exchangeIndex() != 0 || m.sequence != 0 {
		t.Fatal("reclaimed selection must follow newest retained exchange")
	}
	m.snapshot.Exchanges = nil
	if m.exchangeIndex() != -1 {
		t.Fatal("empty retention has an exchange index")
	}
}

func TestAppServerSessionMetricsRefreshAndBounds(t *testing.T) {
	capture, err := capturer.New(capturer.Config{Mode: "mekugi"})
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
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		u.view.painter.Theme = theme
		for tab := range sessionMetricViews {
			u.statusPanel.metrics.tab = tab
			u.renderSessionMetrics()
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
		{name: "storage", width: 90, height: 24},
		{name: "storage-narrow", width: 35, height: 18},
		{name: "throughput-narrow", width: 36, height: 22, top: 18},
		{name: "throughput-absent", width: 90, height: 25, top: 14, throughput: "absent"},
		{name: "throughput-zero", width: 90, height: 25, top: 14, throughput: "zero"},
		{name: "transport", tab: 1, width: 90, height: 55},
		{name: "exchanges", tab: 2, width: 90, height: 60},
		{name: "exchanges-scrolled", tab: 2, width: 90, height: 25, top: 27},
		{name: "provider-tier", tab: 2, width: 90, height: 25, top: 48},
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
			u.view.painter.Theme = livediff.DarkTheme
			u.session.cwd = "/workspace"
			u.replayDebugDirectory = tc.debug
			u.showSessionMetrics()
			m := u.statusPanel.metrics
			m.tab = tc.tab
			if !tc.missing {
				m.snapshot = sessionMetricsFixture(t)
			}
			if strings.HasPrefix(tc.name, "storage") {
				m.snapshot.StorageWrites = &persistence.Snapshot{Bytes: 12345, Scope: new(persistence.Counter).Snapshot().Scope}
			}
			if tc.throughput == "absent" {
				m.snapshot.Usage.OutputThroughput = capturer.OutputThroughput{}
			}
			if tc.throughput == "zero" {
				m.snapshot.Usage.OutputThroughput.OutputTokens = 0
			}
			if tc.empty {
				m.snapshot.Exchanges = nil
			}
			u.renderSessionMetrics()
			u.statusPanel.top = tc.top
			assertNativeUISnapshot(t, "native-session-"+tc.name, u.statusPanelFrame(tc.width, tc.height))
		})
	}
}

func TestUISnapshotNativeSessionShortcuts(t *testing.T) {
	for _, width := range []int{40, 140} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			assertNativeUISnapshot(t, fmt.Sprintf("native-session-shortcuts-%d", width), renderNativeKeybindings(width, 40))
		})
	}
}
