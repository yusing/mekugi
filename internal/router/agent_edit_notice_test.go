package router

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func projectedEditOutput(t *testing.T, request *parsedResponsesRequest, callID string) []map[string]any {
	t.Helper()
	var items []struct {
		CallID string         `json:"call_id"`
		Type   string         `json:"type"`
		Output jsontext.Value `json:"output"`
	}
	if err := json.Unmarshal(request.fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.CallID != callID || !strings.HasSuffix(item.Type, "_output") {
			continue
		}
		var parts []map[string]any
		if err := json.Unmarshal(item.Output, &parts); err != nil {
			var stock string
			if json.Unmarshal(item.Output, &stock) != nil {
				t.Fatalf("projected output for %s = %s: %v", callID, item.Output, err)
			}
			return []map[string]any{{"type": "input_text", "text": stock}}
		}
		return parts
	}
	t.Fatalf("missing projected output for %s", callID)
	return nil
}

func assertEditNotice(t *testing.T, ctx context.Context, proxy *mekugiProxy, workspace string, request *parsedResponsesRequest, outputCall, recordCall, original, filename string, wantNotice bool) {
	t.Helper()
	parts := projectedEditOutput(t, request, outputCall)
	if len(parts) == 0 || parts[0]["type"] != "input_text" || parts[0]["text"] != original {
		t.Fatalf("stock output changed: %+v", parts)
	}
	if !wantNotice {
		if len(parts) != 1 {
			t.Fatalf("no-effect edit received a notice: %+v", parts)
		}
		return
	}
	record, found, err := proxy.replayStore.lookup(ctx, workspace, recordCall)
	if err != nil || !found || record.ChangeID == "" {
		t.Fatalf("missing durable change for %s: %+v found=%v err=%v", recordCall, record, found, err)
	}
	summary, err := proxy.replayStore.readChanges(ctx, changeReadOptions{workspace: workspace, ids: []string{record.ChangeID}, view: "summary"})
	if err != nil {
		t.Fatal(err)
	}
	want := "mchanges " + record.ChangeID + " --summary\n" + summary
	if len(parts) != 2 || parts[1]["type"] != "input_text" || parts[1]["text"] != want || !strings.Contains(summary, filename) {
		t.Fatalf("notice = %+v, want %q", parts, want)
	}
}

func TestAgentEditNoticeNativePatchProjectionAndReplay(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch\n"
	transform := prepareNativeStockTransform(t, proxy, workspace, "notice-patch")
	streamNativePatch(t, transform, patch, nil, nil)
	if err := os.WriteFile(target, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := "Success. Updated the following files:\nM file.txt\n"
	items := []any{
		map[string]any{"type": "custom_tool_call", "id": "patch-item", "call_id": "patch-call", "name": applyPatchToolName, "input": patch, "status": "completed"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "patch-call", "output": original},
	}
	request := parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
	for range 2 {
		if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
			t.Fatal(err)
		}
		assertEditNotice(t, transform.ctx, proxy, workspace, &request, "patch-call", nativePatchDerivedCallID("patch-call", 0), original, "file.txt", true)
	}
}

func TestAgentEditNoticeShellProjection(t *testing.T) {
	for _, effect := range []bool{false, true} {
		t.Run(map[bool]string{false: "no effect", true: "effect"}[effect], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			workspace := t.TempDir()
			target := filepath.Join(workspace, "file.txt")
			if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			transform := prepareNativeStockTransform(t, proxy, workspace, "notice-shell")
			arguments := string(mustMarshalJSON(map[string]any{"cmd": "printf 'new\\n' > file.txt"}))
			streamNativeExecCommand(t, transform, "shell-call", arguments)
			if effect {
				if err := os.WriteFile(target, []byte("new\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			original := nativeExecOutput("Process exited with code 0")
			items := []any{
				map[string]any{"type": "function_call", "id": "shell-call-item", "call_id": "shell-call", "name": nativeExecCommandToolName, "arguments": arguments},
				map[string]any{"type": "function_call_output", "call_id": "shell-call", "output": original},
			}
			request := parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
			if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
				t.Fatal(err)
			}
			assertEditNotice(t, transform.ctx, proxy, workspace, &request, "shell-call", "shell-call:exec:1", original, "file.txt", effect)
		})
	}
}

func TestAgentEditNoticeCodeModeStructuredOutput(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
	transform.sessionShell = "bash"
	target := filepath.Join(workspace, "f")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	call := map[string]any{
		"type": "custom_tool_call", "id": "code-item", "call_id": "code-call", "name": "exec",
		"input": `text(await tools.exec_command({cmd: "printf 'new\\n' > f"}));`, "status": "completed",
	}
	if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "response", "status": "completed", "output": []any{call}})); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := "Script completed\nWall time 0.1 seconds\nOutput:\n"
	request := parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, []any{call, map[string]any{
		"type": "custom_tool_call_output", "call_id": "code-call", "output": []any{map[string]any{"type": "input_text", "text": original}},
	}})}}
	if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	assertEditNotice(t, transform.ctx, proxy, workspace, &request, "code-call", "code-call:effects", original, "f", true)
}

func TestAgentEditNoticeShellContinuation(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	transform := prepareNativeStockTransform(t, proxy, workspace, "notice-continuation")
	arguments := string(mustMarshalJSON(map[string]any{"cmd": "printf 'new\\n' > file.txt"}))
	streamNativeExecCommand(t, transform, "shell-call", arguments)
	running := nativeExecOutput("Process running with session ID 9")
	items := []any{
		map[string]any{"type": "function_call", "id": "shell-call-item", "call_id": "shell-call", "name": nativeExecCommandToolName, "arguments": arguments},
		map[string]any{"type": "function_call_output", "call_id": "shell-call", "output": running},
	}
	request := parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
	if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	assertEditNotice(t, transform.ctx, proxy, workspace, &request, "shell-call", "shell-call:exec:1", running, "file.txt", false)
	if err := os.WriteFile(target, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	completed := nativeExecOutput("Process exited with code 0")
	items = append(items,
		map[string]any{"type": "function_call", "call_id": "stdin-call", "name": "write_stdin", "arguments": `{"session_id":9,"chars":""}`},
		map[string]any{"type": "function_call_output", "call_id": "stdin-call", "output": completed},
	)
	request = parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
	if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	assertEditNotice(t, transform.ctx, proxy, workspace, &request, "stdin-call", "shell-call:exec:1", completed, "file.txt", true)
}

func TestAgentEditNoticeRetainsAllIDsWhenStatisticsTruncate(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	var ids []string
	for index := range 2 {
		call := nativePatchDerivedCallID("many-patches", index)
		id, err := proxy.replayStore.reserveChange(t.Context(), workspace, "author", call)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		var files []mekugi.ReviewFile
		for number := range 16 {
			path := fmt.Sprintf("%d-%d-%s", index, number, strings.Repeat("x", 600))
			files = append(files, mekugi.ReviewFile{AfterPath: path, Diff: "add \"\" -> \"" + path + "\"\n"})
		}
		if err := proxy.replayStore.put(t.Context(), workspace, map[string]mekugiHistory{call: {
			ChangeID: id, CorrelationID: call, ReviewFiles: files,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	notice, err := proxy.replayStore.agentEditNotice(t.Context(), workspace, "many-patches", mekugiHistory{NativePatches: make([]nativePatchObservation, 2)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(notice, "mchanges "+strings.Join(ids, " ")+" --summary\n") || !strings.Contains(notice, "summary truncated") || len(notice) > maxEditReceiptDiffBytes {
		t.Fatalf("notice lost IDs or exceeded its bound: %q", notice)
	}
}
