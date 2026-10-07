package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeExecSedInPlaceCapturesEdit(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	const relativePath = "internal/router/live_activity_view_test.go"
	const before = "package router\n\nvar answer = response.Answers[0].Id\n"
	const after = "package router\n\nvar answer = response.Answers[0].ID\n"
	const command = `sed -i 's/Answers\[0\]\.Id/Answers[0].ID/' internal/router/live_activity_view_test.go`
	target := filepath.Join(workspace, relativePath)
	writeTestFile(t, target, before)
	transform := prepareNativeStockTransform(t, proxy, workspace, "sed-session")
	arguments := string(mustMarshalJSON(map[string]any{"cmd": command, "workdir": workspace}))
	retainCommandObservation(t, transform, "sed-call", arguments)
	if history := transform.local["sed-call"]; history.ExecObservation == nil || history.CarrierPayload != arguments {
		t.Fatalf("sed input was not retained before execution: %+v", history)
	}

	// Execute the exact reported shell shape once, between capture and reconciliation.
	cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-c", command)
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(), "BASH_ENV=")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sed: %v\n%s", err, output)
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != after {
		t.Fatalf("edited content = %q, err = %v", content, err)
	}
	items := []any{
		map[string]any{"type": "function_call", "id": "sed-call-item", "call_id": "sed-call", "name": nativeExecCommandToolName, "arguments": arguments},
		map[string]any{"type": "function_call_output", "call_id": "sed-call", "output": nativeExecOutput("Process exited with code 0")},
	}
	next := reconcileExecItems(t, proxy, workspace, items)
	history, found, err := proxy.replayStore.lookup(t.Context(), workspace, "sed-call:exec:1")
	if err != nil || !found || history.ChangeID == "" || len(history.ReviewFiles) != 1 {
		t.Fatalf("persisted sed edit = %+v, found = %v, err = %v", history, found, err)
	}
	changes, err := proxy.replayStore.readChanges(next.ctx, changeReadOptions{
		workspace: workspace, ids: []string{history.ChangeID}, view: "history", maxTokens: 4000,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{relativePath, "-var answer = response.Answers[0].Id", "+var answer = response.Answers[0].ID", "exec_command input:\n" + command} {
		if !strings.Contains(changes, want) {
			t.Errorf("mchanges history missing %q:\n%s", want, changes)
		}
	}
	receipt := editReceiptText(workspace, history)
	if !strings.HasPrefix(receipt, "Edit `"+relativePath+"`") || !strings.Contains(receipt, " · sed") {
		t.Fatalf("sed receipt should identify an Edit and sed label: %q", receipt)
	}
}
