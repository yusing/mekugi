package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/capturer"
)

func prepareAXSessionFixture(t *testing.T) (session, journal, assessments string) {
	t.Helper()
	codexDirectory, stateDirectory, workspace := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", codexDirectory)
	t.Setenv("XDG_STATE_HOME", stateDirectory)
	store, err := openMekugiReplayStore(filepath.Join(stateDirectory, "mekugi", "replay"))
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{toolName: mekugiToolName, script: "in file.txt\ntype \"old\" \"new\"\n",
		root: workspace, patch: "patch", report: "report", attempt: 2, correlationID: "chain",
		carrierName: "exec", carrierKind: codeModeCarrierFunction, carrierPayload: "{}"}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"edit": history}); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(codexDirectory, "sessions")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	session = filepath.Join(directory, "rollout-synthetic-thread.jsonl")
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	for _, record := range []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"id": "thread", "session_id": "root-thread", "cwd": workspace}},
		{"timestamp": "2026-09-11T00:00:00Z", "type": "event_msg", "payload": map[string]any{"type": "task_started", "turn_id": "turn"}},
		{"type": "response_item", "payload": map[string]any{"type": "function_call", "call_id": "edit", "name": "exec", "arguments": "{}"}},
		{"type": "response_item", "payload": map[string]any{"type": "function_call_output", "call_id": "edit", "output": "report"}},
		{"timestamp": "2026-09-11T00:00:02Z", "type": "event_msg", "payload": map[string]any{"type": "task_complete", "turn_id": "turn"}},
	} {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(session, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	journal = filepath.Join(t.TempDir(), "reads.jsonl")
	// These report-consumer fixtures use the owning capturer's complete events.
	// Actual private-reader dispatch and instrumentation are covered by
	// TestAXObservesExecutedPrivateReaders and the debug worker tests.
	for _, read := range []struct{ thread, tool string }{{"root-thread", "hgrep"}, {"thread", "hcat"}} {
		observation, err := capturer.StartAXRead(journal, read.thread, read.tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := observation.FinishResult(true, "", new(0)); err != nil {
			t.Fatal(err)
		}
	}
	assessments = filepath.Join(directory, "assessments.json")
	if err := os.WriteFile(filepath.Join(directory, "failure.txt"), []byte("TestExample failed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assessments, []byte(`[{"call_id":"edit","verdict":"defect","evidence":"failure.txt"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	return
}

func TestAXSessionReportCombinesEvidenceWithoutWorkspaceFlag(t *testing.T) {
	session, journal, assessments := prepareAXSessionFixture(t)
	var stdout, stderr bytes.Buffer
	code := RunSessionInspection(t.Context(), []string{
		"--session", session, "--read-log", journal, "--defects", assessments,
		"--call-id", "not-selected",
	}, &stdout, &stderr)
	var result sessionInspection
	if code != 0 || json.Unmarshal(stdout.Bytes(), &result) != nil || result.AX == nil {
		t.Fatalf("code %d: %s, output %s", code, stderr.String(), stdout.String())
	}
	ax := result.AX
	if len(result.Calls) != 0 || ax.Edits.Calls != 1 || ax.Edits.RecoveryRetries != 1 ||
		ax.Reads.Succeeded != 1 || ax.Completion.DurationMS != 2000 || ax.Defects.Reported != 1 ||
		ax.Defects.Unassessed != 0 || ax.Edits.Unconfirmed != 0 {
		t.Fatalf("AX scope/evidence = %+v", ax)
	}
}

func TestDebugImpliesAXAndWritesAutomaticReport(t *testing.T) {
	_, journal, _ := prepareAXSessionFixture(t)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv(capturer.AXReadOutputEnvironment, journal)
	flags := newRouterFlags(io.Discard)
	*flags.debug = true
	debug, err := openDebugOutput(flags)
	if err != nil {
		t.Fatal(err)
	}
	debug.observeAXThread("thread")
	debug.observeAXThread("missing")
	if err := debug.close(); err != nil {
		t.Fatal(err)
	}
	if len(debug.paths) != 6 || debug.paths[4] != journal {
		t.Fatalf("debug paths = %v", debug.paths)
	}
	data, err := os.ReadFile(debug.paths[5])
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Schema  string          `json:"schema"`
		Threads []debugAXThread `json:"threads"`
	}
	if err := json.Unmarshal(data, &report); err != nil || report.Schema != "mekugi.ax.debug.v1" || len(report.Threads) != 2 {
		t.Fatalf("automatic AX report = %s, %v", data, err)
	}
	if report.Threads[0].State != "rollout_unavailable" || report.Threads[1].State != "observed" ||
		report.Threads[1].Report.Reads.Succeeded != 1 || report.Threads[1].Report.Edits.Calls != 1 ||
		report.Threads[1].Report.Defects.Unassessed != 1 {
		t.Fatalf("automatic AX evidence = %+v", report)
	}
	if bytes.Contains(data, []byte(`type \"old\"`)) || bytes.Contains(data, []byte("failure.txt")) {
		t.Fatal("automatic report retained source or inferred an assessment")
	}
}

func TestDebugAXRejectsAmbiguousRollouts(t *testing.T) {
	session, _, _ := prepareAXSessionFixture(t)
	data, err := os.ReadFile(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(session), "another-thread.jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	found, err := discoverDebugRollouts(t.Context(), []string{"thread"})
	if err != nil || len(found["thread"]) != 2 {
		t.Fatalf("discovery = %v, %v", found, err)
	}
}

func TestDebugAXDiscoveryRejectsUnreadableSubtree(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode-000 directories")
	}
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	unreadable := filepath.Join(root, "sessions", "unreadable")
	if err := os.MkdirAll(unreadable, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sessions", "rollout-thread.jsonl"), []byte(`{"type":"session_meta","payload":{"id":"thread"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(unreadable, 0700); err != nil {
			t.Error(err)
		}
	})
	// The unreadable subtree could contain a second match. A partial discovery
	// must not certify the visible rollout as the unique source for this thread.
	if _, err := discoverDebugRollouts(t.Context(), []string{"thread"}); err == nil {
		t.Fatal("unreadable subtree silently accepted as complete discovery")
	}
}

func TestSessionRejectsAXCaptureAndMetricsAliases(t *testing.T) {
	for _, debug := range []bool{false, true} {
		for _, target := range []string{"capture", "metrics"} {
			for _, alias := range []bool{false, true} {
				root := t.TempDir()
				t.Setenv("TMPDIR", root)
				journal := filepath.Join(root, "reads.jsonl")
				before := []byte("existing journal bytes\n")
				if err := os.WriteFile(journal, before, 0600); err != nil {
					t.Fatal(err)
				}
				output := journal
				if alias {
					output = filepath.Join(root, "alias")
					if err := os.Link(journal, output); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv(capturer.AXReadOutputEnvironment, journal)
				args := []string{"--" + target + "-output", output}
				if debug {
					args = append(args, "--debug")
				}
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				err := RunSession(ctx, args, nil, nil, nil)
				if err == nil || !strings.Contains(err.Error(), "must use different files") {
					t.Fatalf("debug=%v %s alias=%v: %v", debug, target, alias, err)
				}
				after, err := os.ReadFile(journal)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("journal changed for debug=%v %s alias=%v: %v", debug, target, alias, err)
				}
			}
		}
	}
}
