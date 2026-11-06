package launch

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	inspectioncli "github.com/yusing/mekugi/apps/mekugi/internal/inspectioncli"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

type threadInspectionReport struct {
	TotalCalls int `json:"total_calls"`
	Calls      []struct {
		CallID string                `json:"call_id"`
		Text   map[string]query.Text `json:"text"`
	} `json:"calls"`
	Instructions *diagnostic.InstructionReport `json:"instructions"`
}

func writeThreadRollout(t *testing.T, codexHome, name, thread string) {
	t.Helper()
	directory := filepath.Join(codexHome, "sessions", "2026", "11", "01")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStockRollout(t, filepath.Join(directory, "rollout-2026-11-01T00-00-00-"+name+".jsonl"),
		map[string]any{"type": "session_meta", "payload": map[string]any{"id": thread, "cwd": codexHome}},
		stockRolloutItem(map[string]any{"type": "function_call", "call_id": "read", "name": "shell", "arguments": `{"command":"cat source.txt"}`}),
		stockRolloutItem(map[string]any{"type": "function_call_output", "call_id": "read", "output": "source contents"}),
	)
}

func runThreadInspection(t *testing.T, args ...string) (threadInspectionReport, int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := inspectioncli.RunSessionInspection(testInspector(nil, nil), testDiagnostics(nil, nil), t.Context(), args, &stdout, &stderr)
	var result threadInspectionReport
	if code != 2 && stdout.Len() != 0 {
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("decode inspection: %v; code=%d stderr=%s stdout=%s", err, code, &stderr, &stdout)
		}
	}
	return result, code, stderr.String()
}

func TestSessionInspectionByThread(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	const thread, bare = "thread-1", "bare-thread"
	writeThreadRollout(t, codexHome, thread, thread)
	// The filename ends in "-thread-1" but its metadata names another thread.
	writeThreadRollout(t, codexHome, "other-"+thread, "other-"+thread)
	writeThreadRollout(t, codexHome, bare, bare)

	replay := filepath.Join(t.TempDir(), "replay")
	diagnostics := testDiagnostics(nil, nil)
	store, err := diagnostics.OpenFailures(t.Context(), replay)
	if err != nil {
		t.Fatal(err)
	}
	instructions := "guidance <mekugi_environment_facts>facts</mekugi_environment_facts>"
	if err := diagnostics.RetainInstructions(store, nil, history.Limits{}).Observe(t.Context(), diagnostic.InstructionRequest{
		Thread: thread, Model: "model-a", TurnID: "turn-1", Time: time.Now(),
		Content: func(visitor diagnostic.InstructionVisitor) error {
			visitor.Visit(diagnostic.InstructionText, jsontext.Value(`"`+instructions+`"`))
			visitor.Visit(diagnostic.ToolDescription, jsontext.Value(`{"name":"exec"}`))
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	result, code, stderr := runThreadInspection(t, "--thread", thread, "--replay-dir", replay)
	if code != 0 || stderr != "" || result.TotalCalls != 1 || result.Instructions == nil || result.Instructions.Status != diagnostic.InstructionsAvailable {
		t.Fatalf("thread inspection code=%d stderr=%q result=%+v", code, stderr, result)
	}
	revision := result.Instructions.Revisions[0]
	if text := result.Instructions.Content[revision.Instructions]; text.Text != "" || text.Bytes != len(instructions) {
		t.Fatalf("unselected instruction text = %+v", text)
	}

	result, code, _ = runThreadInspection(t, "--thread", thread, "--replay-dir", replay, "--field", "instructions", "--text-bytes", "8")
	if code != 0 || result.Instructions.Content[revision.Instructions].Text != instructions[:8] || result.Calls[0].Text["script"].Text != "" {
		t.Fatalf("instruction field selection code=%d result=%+v", code, result)
	}
	result, code, _ = runThreadInspection(t, "--thread", thread, "--replay-dir", replay, "--field", "script")
	if code != 0 || result.Instructions.Content[revision.Instructions].Text != "" || result.Calls[0].Text["script"].Text == "" {
		t.Fatalf("call field selection code=%d result=%+v", code, result)
	}

	result, code, stderr = runThreadInspection(t, "--thread", bare, "--replay-dir", replay, "--field", "all")
	if code != 0 || stderr != "" || result.TotalCalls != 1 || result.Instructions.Status != diagnostic.InstructionsUnavailable || result.Instructions.Reason == "" {
		t.Fatalf("unretained thread code=%d stderr=%q result=%+v", code, stderr, result)
	}

	anonymous := filepath.Join(t.TempDir(), "anonymous.jsonl")
	writeStockRollout(t, anonymous, stockRolloutItem(map[string]any{"type": "function_call", "call_id": "read", "name": "shell", "arguments": `{"command":"true"}`}))
	result, code, _ = runThreadInspection(t, "--session", anonymous, "--replay-dir", replay, "--field", "instructions")
	if code != 0 || result.Instructions == nil || result.Instructions.Status != diagnostic.InstructionsUnavailable || !strings.Contains(result.Instructions.Reason, "thread identity") {
		t.Fatalf("rollout without thread identity code=%d result=%+v", code, result)
	}

	if _, code, stderr = runThreadInspection(t, "--thread", "missing-thread", "--replay-dir", replay); code != 1 || !strings.Contains(stderr, "not found") {
		t.Fatalf("unknown thread code=%d stderr=%q", code, stderr)
	}
	for _, args := range [][]string{
		{"--thread", thread, "--session", "rollout.jsonl"},
		{"--thread", thread, "--debug-dir", t.TempDir()},
		{"--thread", thread, "--failures"},
		{"--thread", thread, "--field", "unknown"},
	} {
		if _, code, _ := runThreadInspection(t, args...); code != 2 {
			t.Fatalf("%q code=%d, want 2", args, code)
		}
	}

	// Invalid instruction records are reported beside intact call evidence.
	content := filepath.Join(replay, "instruction-content-"+revision.Instructions+".json")
	if err := os.WriteFile(content, []byte(`"substituted"`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, code, stderr = runThreadInspection(t, "--thread", thread, "--replay-dir", replay)
	if code != 1 || !strings.Contains(stderr, "retained instructions") || result.TotalCalls != 1 ||
		result.Instructions.Status != diagnostic.InstructionsInvalid || !strings.Contains(result.Instructions.Reason, "does not match") {
		t.Fatalf("invalid instructions code=%d stderr=%q result=%+v", code, stderr, result)
	}
}
