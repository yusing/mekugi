package router

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func debugInspectionFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"router.jsonl": `{"request_id":"req-a","event":"request_complete","outcome":"completed"}
{"request_id":"req-b","event":"request_complete","outcome":"failed","error":"original 錯誤 detail","diagnostic_code":"upstream","diagnostic_reference":"failure-1"}
{"request_id":"req-b-extra","event":"request_complete","outcome":"failed","error":"neighbor failure"}
`,
		"capture.jsonl": `{"capture_id":"req-a","boundary":"provider","status_code":200}
{"capture_id":"req-b","boundary":"provider","status_code":502,"response_status":"failed","provider_attempt":2}
`,
		"instructions.jsonl": `{"request_id":"req-a","model":"model-a","instructions":"private unrelated instructions"}
{"capture_id":"req-b","model":"model-b","instructions":"selected instructions","developer_messages":["selected developer"],"tools":[{"name":"selected-tool"}],"additional_tools":["extra"],"wire_developer_messages":["wire developer"],"wire_additional_tools":["wire extra"],"wire_request":"never expose entire request"}
`,
		"metrics.json": `{"schema":"metrics-fixture","requests":{"total":2},"usage":{"input_tokens":12},"secret":"hidden metrics"}`,
		"ax.json":      `{"schema":"ax-fixture","scope":"bundle","threads":["thread-a"],"secret":"hidden ax"}`,
		"reads.jsonl":  "",
	}
	for name, data := range files {
		writeDebugInspectionFixture(t, filepath.Join(dir, name), data)
	}
	return dir
}

func writeDebugInspectionFixture(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func runDebugInspection(t *testing.T, dir string, options ...string) (debugSessionInspection, int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := RunSessionInspection(t.Context(), append([]string{"--debug-dir", dir}, options...), &stdout, &stderr)
	var result debugSessionInspection
	if code != 2 {
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("decode inspection: %v; code=%d stderr=%s stdout=%s", err, code, &stderr, &stdout)
		}
	}
	return result, code, stdout.String(), stderr.String()
}

func TestSessionInspectionDebugSummaryReadOnly(t *testing.T) {
	dir := debugInspectionFixture(t)
	type original struct {
		data []byte
		info os.FileInfo
	}
	before := map[string]original{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		before[path] = original{data, info}
	}
	result, code, output, stderr := runDebugInspection(t, dir)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if result.Schema != "mekugi.session.debug.v1" || result.Directory != dir || len(result.Artifacts) != 6 || result.TotalEvidence != 3 || len(result.Evidence) != 3 {
		t.Fatalf("unexpected summary: %+v", result)
	}
	if result.TotalRequests != 3 || len(result.Requests) != 3 || string(result.Requests[0].Metadata["request_id"]) != `"req-a"` || len(result.Requests[0].Text) != 0 {
		t.Fatalf("healthy request missing from metadata index: %+v", result.Requests)
	}
	if string(result.Metrics["requests"]) != `{"total":2}` || string(result.AX["scope"]) != `"bundle"` {
		t.Fatalf("missing metrics or AX: %+v", result)
	}
	for _, evidence := range result.Evidence {
		if len(evidence.Text) != 0 || evidence.Source == "instructions.jsonl" {
			t.Fatalf("default exposed text: %+v", evidence)
		}
	}
	for _, hidden := range []string{"original 錯誤 detail", "neighbor failure", "private unrelated", "selected instructions", "hidden metrics", "hidden ax", "never expose"} {
		if strings.Contains(output, hidden) {
			t.Fatalf("default exposed %q", hidden)
		}
	}
	for path, original := range before {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, original.data) || info.Mode() != original.info.Mode() || !info.ModTime().Equal(original.info.ModTime()) {
			t.Fatalf("inspection changed %s", path)
		}
	}
	after, err := os.ReadDir(dir)
	if err != nil || len(after) != len(entries) {
		t.Fatalf("inspection created bundle artifacts: %v %v", after, err)
	}
}

func TestSessionInspectionDebugRequestAndBoundedText(t *testing.T) {
	dir := debugInspectionFixture(t)
	result, code, output, stderr := runDebugInspection(t, dir, "--request-id", "req-b", "--field", "diagnostic", "--text-bytes", "12")
	if code != 0 || stderr != "" || result.TotalEvidence != 3 || len(result.Evidence) != 3 {
		t.Fatalf("code=%d stderr=%s result=%+v", code, stderr, result)
	}
	for i, source := range []string{"router.jsonl", "capture.jsonl", "instructions.jsonl"} {
		if result.Evidence[i].Source != source || result.Evidence[i].Line != 2 {
			t.Fatalf("request correlation: %+v", result.Evidence)
		}
	}
	if result.TotalRequests != 1 || len(result.Requests) != 1 || result.Artifacts[0].Counts["outcome/failed"] != 2 || result.Artifacts[0].Counts["outcome/completed"] != 1 {
		t.Fatalf("selection changed whole-artifact counts or request index: %+v", result)
	}
	text := result.Evidence[0].Text["error"]
	if text.Text != "original 錯" || text.Bytes != len("original 錯誤 detail") || text.OmittedBytes != text.Bytes-len(text.Text) || !utf8.ValidString(text.Text) {
		t.Fatalf("error not bounded at UTF-8 boundary: %+v", text)
	}
	if len(result.Evidence[2].Text) != 0 || strings.Contains(output, "neighbor failure") || strings.Contains(output, "selected instructions") {
		t.Fatalf("diagnostic included unrelated text: %s", output)
	}
	result, code, output, stderr = runDebugInspection(t, dir, "--request-id", "req-b", "--field", "all", "--text-bytes", "8")
	if code != 0 || stderr != "" || len(result.Evidence[2].Text) != 6 {
		t.Fatalf("selected instruction fields missing: code=%d stderr=%s result=%+v", code, stderr, result)
	}
	for _, evidence := range result.Evidence {
		for key, text := range evidence.Text {
			if len(text.Text) > 8 || text.Bytes != len(text.Text)+text.OmittedBytes || !utf8.ValidString(text.Text) {
				t.Fatalf("unbounded %s: %+v", key, text)
			}
		}
	}
	if strings.Contains(output, "private unrelated") || strings.Contains(output, "never expose entire request") {
		t.Fatalf("selected all leaked other content: %s", output)
	}
	result, code, _, stderr = runDebugInspection(t, dir, "--field", "all")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	for _, evidence := range result.Evidence {
		if evidence.Source == "instructions.jsonl" {
			t.Fatal("all without selected request exposed instructions")
		}
	}
}

func TestSessionInspectionDebugPagination(t *testing.T) {
	dir := debugInspectionFixture(t)
	for _, offset := range []string{"0", "1", "2", "8"} {
		t.Run(offset, func(t *testing.T) {
			result, code, _, stderr := runDebugInspection(t, dir, "--offset", offset, "--limit", "1")
			if code != 0 || stderr != "" || result.TotalEvidence != 3 || result.TotalRequests != 3 {
				t.Fatalf("code=%d stderr=%s result=%+v", code, stderr, result)
			}
			if offset == "8" {
				if len(result.Evidence) != 0 || len(result.Requests) != 0 || result.NextOffset != nil {
					t.Fatalf("out-of-range page: %+v", result)
				}
				return
			}
			if len(result.Evidence) != 1 || len(result.Requests) != 1 || (offset == "2") != (result.NextOffset == nil) {
				t.Fatalf("page: %+v", result)
			}
			if result.NextOffset != nil && *result.NextOffset != result.Offset+1 {
				t.Fatalf("next offset: %+v", result)
			}
		})
	}
}

func TestSessionInspectionDebugTimeoutCancellationAndIncomplete(t *testing.T) {
	dir := debugInspectionFixture(t)
	writeDebugInspectionFixture(t, filepath.Join(dir, "router.jsonl"), `{"request_id":"healthy","event":"request_complete","outcome":"completed"}
{"request_id":"timeout","event":"request_complete","outcome":"timed_out"}
{"request_id":"cancelled","event":"request_complete","outcome":"cancelled","cancellation_cause":"user"}
{"event":"shutdown","failed":true}
`)
	writeDebugInspectionFixture(t, filepath.Join(dir, "capture.jsonl"), `{"capture_id":"incomplete","response_status":"incomplete","status_code":200}`+"\n")
	result, code, _, stderr := runDebugInspection(t, dir)
	if code != 0 || stderr != "" || result.TotalRequests != 3 || result.TotalEvidence != 4 {
		t.Fatalf("non-success evidence missing: code=%d stderr=%s result=%+v", code, stderr, result)
	}
	for i, line := range []int{2, 3, 4, 1} {
		if result.Evidence[i].Line != line {
			t.Fatalf("unexpected evidence: %+v", result.Evidence)
		}
	}
}

func TestSessionInspectionDebugIndependentArtifacts(t *testing.T) {
	dir := debugInspectionFixture(t)
	writeDebugInspectionFixture(t, filepath.Join(dir, "router.jsonl"), "{broken\n")
	writeDebugInspectionFixture(t, filepath.Join(dir, "metrics.json"), "null")
	if err := os.Remove(filepath.Join(dir, "instructions.jsonl")); err != nil {
		t.Fatal(err)
	}
	result, code, _, stderr := runDebugInspection(t, dir)
	if code != 1 || result.TotalEvidence != 1 || len(result.Evidence) != 1 || result.Evidence[0].Source != "capture.jsonl" || len(result.AX) == 0 {
		t.Fatalf("independent artifact results lost: code=%d stderr=%s result=%+v", code, stderr, result)
	}
	for _, name := range []string{"router.jsonl", "metrics.json", "instructions.jsonl"} {
		if !strings.Contains(stderr, filepath.Join(dir, name)+":") {
			t.Fatalf("missing path-qualified failure for %s: %s", name, stderr)
		}
	}
	if len(result.Artifacts) != 6 {
		t.Fatalf("artifacts not all attempted: %+v", result.Artifacts)
	}
	for _, artifact := range result.Artifacts {
		want := "observed"
		if artifact.Name == "router.jsonl" || artifact.Name == "metrics.json" || artifact.Name == "instructions.jsonl" {
			want = "invalid_or_unavailable"
		}
		if artifact.State != want {
			t.Fatalf("artifact state: %+v want %s", artifact, want)
		}
	}
}

func TestSessionInspectionDebugFeatureOutcomesAreNotRequestOutcomes(t *testing.T) {
	dir := debugInspectionFixture(t)
	writeDebugInspectionFixture(t, filepath.Join(dir, "router.jsonl"), `{"event":"request_complete","request_id":"healthy","outcome":"completed"}
{"event":"feature_usage","schema_version":1,"feature":"journal","source":"tool","stage":"mutation","outcome":"accepted"}
{"event":"feature_usage","schema_version":1,"feature":"journal","source":"code_mode","stage":"mutation","outcome":"prepared"}
{"event":"feature_usage","schema_version":1,"feature":"commentary","source":"provider_message","stage":"authored","outcome":"observed"}
{"event":"feature_usage","schema_version":1,"feature":"journal","source":"code_mode","stage":"mutation","outcome":"rejected"}
{"event":"feature_usage","schema_version":1,"feature":"journal","source":"code_mode","stage":"lowering","outcome":"unavailable"}
{"event":"feature_usage","schema_version":1,"feature":"commentary","source":"shell","stage":"publication","outcome":"oversized"}
{"event":"feature_usage","schema_version":1,"feature":"commentary","source":"code_mode","stage":"publication","outcome":"capacity"}
`)
	writeDebugInspectionFixture(t, filepath.Join(dir, "capture.jsonl"), "")
	result, code, _, stderr := runDebugInspection(t, dir)
	if code != 0 || stderr != "" || result.TotalRequests != 1 || result.TotalEvidence != 4 || len(result.Evidence) != 4 {
		t.Fatalf("feature evidence classification: code=%d stderr=%s result=%+v", code, stderr, result)
	}
	for i, outcome := range []string{"rejected", "unavailable", "oversized", "capacity"} {
		evidence := result.Evidence[i]
		if evidence.Source != "router.jsonl" || evidence.Line != i+5 || string(evidence.Metadata["outcome"]) != `"`+outcome+`"` {
			t.Fatalf("feature failure lost: %+v", evidence)
		}
	}
	counts := result.Artifacts[0].Counts
	if counts["outcome/completed"] != 1 {
		t.Fatalf("request count missing: %+v", counts)
	}
	for _, outcome := range []string{"accepted", "prepared", "observed", "rejected", "unavailable", "oversized", "capacity"} {
		if counts["outcome/"+outcome] != 0 {
			t.Fatalf("feature %s attributed to request outcomes: %+v", outcome, counts)
		}
	}
}

func TestSessionInspectionDebugCaptureStreamAndControlOutcomes(t *testing.T) {
	for _, boundary := range []string{"provider", "codex"} {
		for _, status := range []string{"", "cancelled", "error", "incomplete", "failed", "completed"} {
			t.Run(boundary+"/"+status, func(t *testing.T) {
				dir := debugInspectionFixture(t)
				writeDebugInspectionFixture(t, filepath.Join(dir, "router.jsonl"), "")
				row := map[string]any{"boundary": boundary, "capture_id": "stream", "status_code": 200, "response_complete": false}
				if status != "" {
					row["response_status"] = status
					row["response_complete"] = true
				}
				data, err := json.Marshal(&row)
				if err != nil {
					t.Fatal(err)
				}
				writeDebugInspectionFixture(t, filepath.Join(dir, "capture.jsonl"), string(data)+"\n")
				result, code, _, stderr := runDebugInspection(t, dir)
				want := 1
				if status == "completed" {
					want = 0
				}
				if code != 0 || stderr != "" || result.TotalEvidence != want || len(result.Evidence) != want {
					t.Fatalf("stream outcome: code=%d stderr=%s result=%+v", code, stderr, result)
				}
			})
		}
	}
	for _, boundary := range []string{"provider_control", "codex_control"} {
		for _, captureError := range []string{"", "measurement failed"} {
			t.Run(boundary+"/"+captureError, func(t *testing.T) {
				dir := debugInspectionFixture(t)
				writeDebugInspectionFixture(t, filepath.Join(dir, "router.jsonl"), "")
				row := map[string]any{"boundary": boundary, "response_status": "control", "response_complete": captureError == ""}
				if captureError != "" {
					row["capture_error"] = captureError
				}
				data, err := json.Marshal(&row)
				if err != nil {
					t.Fatal(err)
				}
				writeDebugInspectionFixture(t, filepath.Join(dir, "capture.jsonl"), string(data)+"\n")
				result, code, _, stderr := runDebugInspection(t, dir)
				want := 0
				if captureError != "" {
					want = 1
				}
				if code != 0 || stderr != "" || result.TotalEvidence != want || len(result.Evidence) != want {
					t.Fatalf("control outcome: code=%d stderr=%s result=%+v", code, stderr, result)
				}
				if want == 1 && string(result.Evidence[0].Metadata["capture_error"]) != `"measurement failed"` {
					t.Fatalf("control measurement failure lost: %+v", result.Evidence)
				}
			})
		}
	}
}

func TestSessionInspectionDebugRejectsInvalidArtifacts(t *testing.T) {
	for _, name := range []string{"router.jsonl", "metrics.json", "reads.jsonl"} {
		for _, bad := range []string{"malformed", "array", "null", "directory", "fifo"} {
			t.Run(name+"/"+bad, func(t *testing.T) {
				dir := debugInspectionFixture(t)
				path := filepath.Join(dir, name)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				switch bad {
				case "directory":
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				case "fifo":
					if err := syscall.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				default:
					data := map[string]string{"malformed": "{", "array": "[]", "null": "null"}[bad]
					writeDebugInspectionFixture(t, path, data+"\n")
				}
				done := make(chan struct{})
				var stdout, stderr bytes.Buffer
				var code int
				go func() {
					code = RunSessionInspection(t.Context(), []string{"--debug-dir", dir}, &stdout, &stderr)
					close(done)
				}()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("inspection blocked on invalid artifact")
				}
				if code != 1 || !strings.Contains(stderr.String(), path+":") || stdout.Len() == 0 {
					t.Fatalf("invalid artifact accepted: code=%d stderr=%s stdout=%s", code, &stderr, &stdout)
				}
			})
		}
	}
}

func TestSessionInspectionDebugExternalArtifacts(t *testing.T) {
	dir := debugInspectionFixture(t)
	external := t.TempDir()
	readLog := filepath.Join(external, "external-reads.jsonl")
	writeDebugInspectionFixture(t, readLog, "")
	data, err := json.Marshal(map[string]any{"schema": "ax-fixture", "read_log": readLog})
	if err != nil {
		t.Fatal(err)
	}
	writeDebugInspectionFixture(t, filepath.Join(dir, "ax.json"), string(data))
	writeDebugInspectionFixture(t, filepath.Join(dir, "reads.jsonl"), "invalid local read log\n")
	capture := filepath.Join(external, "external-capture.jsonl")
	writeDebugInspectionFixture(t, capture, `{"capture_id":"external-request","status_code":503}`+"\n")
	if err := os.Remove(filepath.Join(dir, "capture.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(capture, filepath.Join(dir, "capture.jsonl")); err != nil {
		t.Fatal(err)
	}
	result, code, _, stderr := runDebugInspection(t, dir, "--request-id", "external-request")
	if code != 0 || stderr != "" || result.TotalEvidence != 1 || result.Evidence[0].Source != "capture.jsonl" {
		t.Fatalf("external selection: code=%d stderr=%s result=%+v", code, stderr, result)
	}
	if result.Artifacts[5].Path != readLog || result.Artifacts[5].State != "observed" {
		t.Fatalf("absolute AX read log ignored: %+v", result.Artifacts[5])
	}
	_, code, _, stderr = runDebugInspection(t, dir, "--request-id", "missing")
	if code != 1 || !strings.Contains(stderr, dir+":") || !strings.Contains(stderr, `request "missing"`) {
		t.Fatalf("missing request: code=%d stderr=%s", code, stderr)
	}
}

func TestSessionInspectionDebugInvalidOptions(t *testing.T) {
	dir := debugInspectionFixture(t)
	options := [][]string{
		{"--session", "rollout"}, {"--failures"}, {"--ax"}, {"--read-log", "reads"}, {"--defects", "defects"},
		{"--call-id", "call"}, {"--workspace", "workspace"}, {"--replay-dir", "replay"}, {"extra"},
		{"--offset", "-1"}, {"--limit", "0"}, {"--limit", "501"}, {"--text-bytes", "0"}, {"--text-bytes", "65537"},
		{"--field", "script"}, {"--field", "report"}, {"--field", "output"}, {"--field", "unknown"},
	}
	for _, args := range options {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, code, stdout, stderr := runDebugInspection(t, dir, args...)
			if code != 2 || stdout != "" || stderr == "" {
				t.Fatalf("invalid options: code=%d stderr=%s stdout=%s", code, stderr, stdout)
			}
		})
	}
	var stdout, stderr bytes.Buffer
	if code := RunSessionInspection(t.Context(), []string{"--session", "unused", "--request-id", "req-b"}, &stdout, &stderr); code != 2 {
		t.Fatalf("request-id without debug-dir: code=%d stderr=%s", code, &stderr)
	}
}
