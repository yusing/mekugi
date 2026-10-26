package executionobservation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

func TestCaptureWithoutGitWorkspaceMetadata(t *testing.T) {
	for _, admission := range []string{"unique", "unknown", "ambiguous"} {
		t.Run(admission, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			workspace := t.TempDir()
			proxy.review.IncludeThread(t.TempDir(), "other", true)
			if admission != "unknown" {
				proxy.review.IncludeThread(workspace, "thread", false)
			}
			if admission == "ambiguous" {
				proxy.review.IncludeThread(t.TempDir(), "thread", false)
			}
			request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
				"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]any{"role": "user", "content": "task"}},
				"tools": []any{map[string]any{"type": "function", "name": "lookup"}}, "tool_choice": "auto",
			}))
			if err != nil {
				t.Fatal(err)
			}
			// Codex's workspace metadata is empty outside Git repositories.
			transform, err := proxy.prepareRequest(t.Context(), &request, "session", "thread", codexresponses.TurnMetadata{RequestKind: "turn", ThreadID: "thread"}, true)
			if err != nil {
				t.Fatal(err)
			}
			defer transform.Close()
			transform.sessionShell = "/bin/bash"
			source := `text(await tools.mcp__threadcontrol__send_message({target:"/root",message:"progress"})); text(await tools.exec_command({cmd:"pwd; printf managed-child-write > managed-child.txt",shell:"/bin/bash",login:false}));`
			_, err = transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{map[string]any{
				"type": "custom_tool_call", "name": "exec", "call_id": "call", "input": source, "status": "completed",
			}}}))
			if err != nil {
				t.Fatal(err)
			}
			observation := transform.local["call"].ExecObservation
			if admission != "unique" {
				if transform.directory != "" || observation != nil {
					t.Fatal("missing workspace metadata borrowed an unproven thread scope")
				}
				return
			}
			if transform.directory != workspace || observation == nil || len(observation.Files) != 1 || observation.Files[0].Path != filepath.Join(workspace, "managed-child.txt") {
				t.Fatalf("admitted non-Git workspace lost its named before-window: cwd=%q observation=%+v", transform.directory, observation)
			}
		})
	}
}

func TestCaptureAfterAdmittedWorkspaceRemoval(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace, commandDirectory := t.TempDir(), t.TempDir()
	proxy.review.IncludeThread(workspace, "stock-thread", false)
	if err := os.Remove(workspace); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(commandDirectory, "file.txt")
	writeTestFile(t, target, "before\n")
	transform := prepareNativeStockTransform(t, proxy, workspace, "removed-workspace")
	source := fmt.Sprintf(`text(await tools.exec_command({cmd:"printf 'after\\n' > file.txt",workdir:%q}));`, commandDirectory)
	_, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{map[string]any{
		"type": "custom_tool_call", "name": "exec", "call_id": "edit", "input": source, "status": "completed",
	}}}))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, target, "after\n")
	history := transform.local["edit"]
	if err := finalizeExecObservations(proxy.retained, proxy.changeSource, proxy.snapshots, proxy.recovered, proxy.publishChanges, proxy.execWindows, proxy.activity, proxy.noticeSink, transform.ctx, transform.directory, []execCompletion{{
		callID: "edit", history: history, result: binding.DecodeExecutionResult(nativeExecCommandToolName, mustTestJSON(t, map[string]any{"exit_code": 0})),
	}}); err != nil {
		t.Fatal(err)
	}
	store := testProxyStore(proxy).scoped(transform.ctx)
	record, found, err := store.lookup(transform.ctx, workspace, legacycall.ExecCallID("edit", true))
	if err != nil || !found || record.ChangeID == "" {
		t.Fatalf("capture lost its admitted workspace: found=%v record=%+v err=%v", found, record, err)
	}
	text, err := store.changes.Read(transform.ctx, store.changeScope(), changes.Query{Workspace: workspace, IDs: []string{record.ChangeID}})
	if err != nil || !strings.Contains(text, "-before\n+after\n") {
		t.Fatalf("receipt ID is unreadable in its removed workspace: %q, %v", text, err)
	}
}

func TestCaptureRemovedWorkspaceCompactionCompletion(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace, commandDirectory := t.TempDir(), t.TempDir()
	proxy.review.IncludeThread(workspace, "stock-thread", false)
	if err := os.Remove(workspace); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(commandDirectory, "file.txt")
	writeTestFile(t, target, "before\n")
	transform := prepareNativeStockTransform(t, proxy, workspace, "removed-compaction")
	arguments := string(mustTestJSON(t, map[string]any{"cmd": "printf 'after\\n' > file.txt", "workdir": commandDirectory}))
	retainCommandObservation(t, transform, "edit", arguments)
	writeTestFile(t, target, "after\n")
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": []any{
			map[string]any{"type": "function_call", "call_id": "edit", "name": nativeExecCommandToolName, "arguments": arguments, "status": "completed"},
			map[string]any{"type": "function_call_output", "call_id": "edit", "output": nativeExecOutput("Process exited with code 0")},
		}}))
	if err != nil {
		t.Fatal(err)
	}
	metadata := codexresponses.TurnMetadata{RequestKind: "compaction", Directories: map[string]json.RawMessage{workspace: nil}}
	if err := proxy.observeCompactionInput(t.Context(), &request, "compaction-session", "stock-thread", metadata); err != nil {
		t.Fatalf("completed edit under admitted removed workspace not retained in compaction: %v", err)
	}
	record, found, err := testProxyStore(proxy).scoped(transform.ctx).lookup(transform.ctx, workspace, legacycall.ExecCallID("edit", false))
	if err != nil || !found || record.ChangeID == "" {
		t.Fatalf("missing retained completion: found=%v record=%+v err=%v", found, record, err)
	}
}
