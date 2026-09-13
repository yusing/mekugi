package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/shellruntime"
)

func TestAXReaderFailureClassesPreserveOutputAndCallIdentity(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	root := t.TempDir()
	missing := shellQuoteArgument(filepath.Join(root, "private-missing"))
	script := "hgrep --max-tokens 16000 secret; hsymbol refs --workspace; hcat " + missing + "; hcat @shell/missing; inspect_file " + missing
	t.Setenv(capturer.AXReadOutputEnvironment, "")
	t.Setenv(shellruntime.ThreadIDEnvironment, "child-thread")
	wantOut, wantErr, wantCode := runShellWorkerTest(t, registry, "bash", nil, script, nil)
	journal := filepath.Join(root, "reads.jsonl")
	t.Setenv(capturer.AXReadOutputEnvironment, journal)
	t.Setenv(capturer.AXCallIDEnvironment, "call-batch")
	out, stderr, code := runShellWorkerTest(t, registry, "bash", nil, script, nil)
	if out != wantOut || stderr != wantErr || code != wantCode {
		t.Fatalf("instrumentation changed command outcome: %d/%d %q/%q %q/%q", code, wantCode, out, wantOut, stderr, wantErr)
	}
	reads, err := capturer.ReadAXReads(t.Context(), journal, "child-thread")
	if err != nil || reads.Failed != 5 || reads.FailuresByClass["invalid_arguments"] != 2 || reads.FailuresByClass["not_found"] != 2 || reads.FailuresByClass["retained_file"] != 1 {
		t.Fatalf("unexplained failures: %+v %v", reads, err)
	}
	shellID := reads.Failures[0].ShellID
	for _, failure := range reads.Failures {
		if failure.CallID != "call-batch" || shellID == "" || failure.ShellID != shellID || failure.ExitCode == nil {
			t.Fatalf("uncorrelated failure: %+v", failure)
		}
	}
	data, _ := os.ReadFile(journal)
	if bytes.Contains(data, []byte(root)) || bytes.Contains(data, []byte("secret")) || bytes.Contains(data, []byte("--workspace")) {
		t.Fatal("private read details leaked")
	}
}

func TestAXDebugWorkerPinsJournalAcrossChildEnvironment(t *testing.T) {
	d := featureDebugOutput(t)
	t.Setenv(shellruntime.RuntimeDirectoryEnvironment, t.TempDir())
	ctx := context.WithValue(t.Context(), debugContextKey{}, d)
	registry, err := buildToolRegistry(ctx, filepath.Join(t.TempDir(), "data"), testMekugiToolDescription, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	t.Setenv(capturer.AXReadOutputEnvironment, "")
	for _, thread := range []string{"parent", "child"} {
		t.Setenv(shellruntime.ThreadIDEnvironment, thread)
		_, _, code := runShellWorkerTest(t, registry, "bash", nil, "hcat /private-missing", nil)
		if code == 0 {
			t.Fatal("missing read succeeded")
		}
		reads, err := capturer.ReadAXReads(t.Context(), d.paths[4], thread)
		if err != nil || reads.Failed != 1 {
			t.Fatalf("child lost instrumentation: %+v %v", reads, err)
		}
	}
}

func TestAXTestProcessDoesNotInheritLiveJournal(t *testing.T) {
	const marker = "MEKUGI_TEST_AX_ISOLATION"
	if os.Getenv(marker) == "1" {
		if os.Getenv(capturer.AXReadOutputEnvironment) != "" {
			t.Fatal("test inherited live journal")
		}
		registry := sharedProxyTestRegistry(t)
		_, _, _ = runShellWorkerTest(t, registry, "bash", nil, "hcat /private-missing", nil)
		return
	}
	path := filepath.Join(t.TempDir(), "live.jsonl")
	if err := os.WriteFile(path, []byte("live session marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestAXTestProcessDoesNotInheritLiveJournal$")
	command.Env = append(os.Environ(), marker+"=1", capturer.AXReadOutputEnvironment+"="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "live session marker\n" {
		t.Fatalf("test polluted live journal: %q %v", data, err)
	}
}

func TestAXDebugLabelsJournalOnlyAndAnonymousEvidence(t *testing.T) {
	d := featureDebugOutput(t)
	t.Setenv("CODEX_HOME", t.TempDir())
	d.observeAXThread("known")
	for _, thread := range []string{"known", "not-a-known-child", ""} {
		read, err := capturer.StartAXRead(d.paths[4], thread, "hcat")
		if err != nil {
			t.Fatal(err)
		}
		if err := read.Finish(false); err != nil {
			t.Fatal(err)
		}
	}
	// Write directly; featureDebugOutput owns ordinary shutdown/cleanup.
	if err := d.writeAXReport(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(d.paths[5])
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Threads     []debugAXThread        `json:"threads"`
		JournalOnly []debugAXThread        `json:"journal_only_threads"`
		Anonymous   capturer.AXReadMetrics `json:"unattributed_reads"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Threads) != 1 || len(report.JournalOnly) != 1 || report.JournalOnly[0].ThreadID != "not-a-known-child" || report.JournalOnly[0].Reads.Failed != 1 || report.Anonymous.Failed != 1 {
		t.Fatalf("silently dropped or guessed attribution: %s", data)
	}
}

func TestDebugCancellationCausesAndCleanup(t *testing.T) {
	request, cancelRequest := context.WithCancel(t.Context())
	server, cancelServer := context.WithCancel(t.Context())
	start, execution, cleanup := requestContexts(request, server, time.Hour)
	cancelServer()
	<-execution.Done()
	if got := requestCancellationCause(execution, execution.Err()); got != "router_shutdown" {
		t.Fatal(got)
	}
	cleanup()
	cancelRequest()
	request, cancelRequest = context.WithCancel(t.Context())
	start, execution, cleanup = requestContexts(request, t.Context(), time.Hour)
	cancelRequest()
	<-execution.Done()
	if got := requestCancellationCause(execution, execution.Err()); got != "downstream_context_canceled" {
		t.Fatal(got)
	}
	cleanup()
	start, execution, cleanup = requestContexts(t.Context(), t.Context(), time.Nanosecond)
	<-start.Done()
	if got := requestCancellationCause(execution, errors.Join(start.Err(), context.Cause(start))); got != "response_start_timeout" {
		t.Fatal(got)
	}
	if got := requestCancellationCause(execution, context.DeadlineExceeded); got != "deadline_unknown" {
		t.Fatalf("guessed from expired start timer: %s", got)
	}
	cleanup()
	for _, tc := range []struct {
		err  error
		want string
	}{{nil, ""}, {errUpstreamStreamIdleTimeout, "upstream_idle_timeout"}, {context.Canceled, "cancellation_unknown"}} {
		if got := requestCancellationCause(context.Background(), tc.err); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}

func TestDebugRequestUsesCaptureCorrelation(t *testing.T) {
	d := featureDebugOutput(t)
	recorder, err := capturer.New(capturer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	var id string
	handler := recorder.Handler(d.handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, id = debugRequest(r.Context())
		captureID, sequence := capturer.RequestCorrelation(r.Context())
		if id == "" || id != captureID || sequence != 1 {
			t.Fatalf("uncorrelated exchange %q %q %d", id, captureID, sequence)
		}
		_, _ = io.WriteString(w, `{"status":"completed","output":[]}`)
	})))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test","input":[]}`)))
	if id == "" {
		t.Fatal("capture boundary not exercised")
	}
}

func TestAXCommandInspectionJoinsLiteralCarrierOnly(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{axCarrierCallIDPrefix + "call-batch\n" + `MEKUGI_AX_CALL_ID='call-batch' shell bash 'hcat secret'`, "call-batch"},
		{`MEKUGI_AX_CALL_ID=$(echo secret) shell bash 'hcat secret'`, ""},
		{`echo MEKUGI_AX_CALL_ID=secret`, ""},
		{`MEKUGI_AX_CALL_ID='/private/path' shell bash ':'`, ""},
	} {
		if got := inspectionAXCallID(mustMarshalJSON(tc.command)); got != tc.want {
			t.Fatalf("%q -> %q", tc.command, got)
		}
	}
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	var records bytes.Buffer
	encoder := json.NewEncoder(&records)
	_ = encoder.Encode(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "thread"}})
	for _, tc := range []struct {
		kind, id string
		ms       int
	}{{"item_started", "one", 0}, {"item_completed", "one", 100}, {"item_started", "two", 400}, {"item_completed", "two", 500}} {
		_ = encoder.Encode(map[string]any{"timestamp": time.Date(2026, 1, 1, 0, 0, 0, tc.ms*1000000, time.UTC).Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": tc.kind, "item": map[string]any{"type": "CommandExecution", "id": tc.id, "command": []string{"/bin/bash", "-lc", axCarrierCallIDPrefix + "call-batch\nMEKUGI_AX_CALL_ID='call-batch' shell bash 'hcat private'"}, "exit_code": 0}}})
	}
	if err := os.WriteFile(path, records.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := RunSessionInspection(t.Context(), []string{"--session", path, "--ax", "--replay-dir", t.TempDir()}, &out, &stderr); code != 0 {
		t.Fatalf("%d: %s", code, &stderr)
	}
	var report sessionInspection
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.AX.Commands.GapMS != 300 || report.AX.Commands.DurationMS != 200 || report.AX.Commands.Commands[1].LogicalCallID != "call-batch" || bytes.Contains(out.Bytes(), []byte("hcat private")) {
		t.Fatalf("bad sanitized timings: %s", &out)
	}
}

func TestDebugCommentaryCoverageAndRouterOrigin(t *testing.T) {
	d := featureDebugOutput(t)
	trace := featureUsageTrace{debug: d, requestID: "request", threadID: "root", summary: &featureUsageSummary{counts: map[string]uint64{}}}
	trace.finish(false, false)
	trace.finish(true, false)
	trace.finish(true, true)
	activity := newSubagentActivity()
	activity.observe("root", "", "/root", false)
	activity.observe("child", "root", "/root/worker", true)
	activity.collect("child", "source", "operation", "private activity text")
	transform := &mekugiResponseTransform{proxy: &mekugiProxy{activity: activity}, threadID: "root", featureTrace: trace}
	messages := transform.drainActivity()
	if len(messages) != 1 {
		t.Fatal("no projected activity")
	}
	events := readFeatureUsage(t, d)
	if len(events) != 1 || events[0]["source"] != "router_activity" || events[0]["stage"] != "render" || events[0]["message_id"] != jsonString(messages[0], "id") {
		t.Fatalf("invented authored commentary: %v", events)
	}
	data, err := os.ReadFile(d.paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var states []string
	for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte{'\n'}) {
		var event map[string]json.RawMessage
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if jsonString(event, "event") == "feature_coverage" {
			states = append(states, jsonString(event, "state"))
			if string(event["observations"]) != "{}" {
				t.Fatal("zero observation marker contains invented usage")
			}
		}
	}
	if strings.Join(states, ",") != "unavailable,incomplete,observed" || bytes.Contains(data, []byte("private activity text")) {
		t.Fatalf("invalid coverage or leaked payload: %s", data)
	}
}

func TestDebugCanceledRequestHasCauseWithoutReplayDiagnostic(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "downstream", true: "start_timeout"}[timeout], func(t *testing.T) {
			d := featureDebugOutput(t)
			parent, cancelParent := context.WithCancel(context.WithValue(t.Context(), debugContextKey{}, d))
			defer cancelParent()
			limit := time.Hour
			if timeout {
				limit = time.Nanosecond
			}
			start, execution, cleanup := requestContexts(parent, t.Context(), limit)
			defer cleanup()
			if !timeout {
				cancelParent()
			}
			<-start.Done()
			if err := executeRequest(start, execution, parsedResponsesRequest{}, http.Header{}, "session", nil, io.Discard, nil, nil, nil, nil); err == nil {
				t.Fatal("canceled request succeeded")
			}
			data, err := os.ReadFile(d.paths[0])
			if err != nil {
				t.Fatal(err)
			}
			want := "downstream_context_canceled"
			if timeout {
				want = "response_start_timeout"
			}
			found := false
			for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte{'\n'}) {
				var event map[string]json.RawMessage
				if err := json.Unmarshal(line, &event); err != nil {
					t.Fatal(err)
				}
				if jsonString(event, "event") == "request_complete" {
					found = true
					if jsonString(event, "cancellation_cause") != want || event["duration_ms"] == nil || event["diagnostic_reference"] != nil {
						t.Fatalf("missing independent cause: %s", line)
					}
				}
			}
			if !found {
				t.Fatal("missing request completion")
			}
		})
	}
}
