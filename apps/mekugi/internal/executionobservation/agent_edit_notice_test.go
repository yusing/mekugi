package executionobservation

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/libraries/legacycall"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/changehistory/changes"

	jsonv1 "encoding/json"
	"encoding/json/jsontext"

	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

func projectedEditOutput(t *testing.T, request *codexresponses.Request, callID string) []map[string]any {
	t.Helper()
	var items []struct {
		CallID string         `json:"call_id"`
		Type   string         `json:"type"`
		Output jsontext.Value `json:"output"`
	}
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
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

func assertEditNotice(t *testing.T, ctx context.Context, proxy *Preparer, workspace string, request *codexresponses.Request, outputCall, recordCall, original, filename string, wantNotice bool) {
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
	record, found, err := testProxyStore(proxy).lookup(ctx, workspace, recordCall)
	if err != nil || !found || record.ChangeID == "" {
		t.Fatalf("missing durable change for %s: %+v found=%v err=%v", recordCall, record, found, err)
	}
	summary, err := testProxyStore(proxy).changes.Read(ctx, testProxyStore(proxy).scoped(ctx).changeScope(), changes.Query{Workspace: workspace, IDs: []string{record.ChangeID}, View: "summary"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("receipt bytes: old=%d new=%d", len("mchanges "+record.ChangeID+" --summary\n"+summary), len(record.ChangeID))
	want := record.ChangeID
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
	retainPatchObservation(t, transform, patch)
	if err := os.WriteFile(target, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := "Success. Updated the following files:\nM file.txt\n"
	items := []any{
		map[string]any{"type": "custom_tool_call", "id": "patch-item", "call_id": "patch-call", "name": applyPatchToolName, "input": patch, "status": "completed"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "patch-call", "output": original},
	}
	request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
	for range 2 {
		if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
			t.Fatal(err)
		}
		assertEditNotice(t, transform.ctx, proxy, workspace, &request, "patch-call", legacycall.PatchCallID("patch-call", 0), original, "file.txt", true)
	}
}

func TestAgentEditNoticeIncompletePatchProjection(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "file.txt")
	writeTestFile(t, target, "old\n")
	if err := os.Truncate(target, changecapture.MaxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch\n"
	transform := prepareNativeStockTransform(t, proxy, workspace, "partial-patch")
	retainPatchObservation(t, transform, patch)
	writeTestFile(t, target, "new\n")
	original := "native patch result"
	request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, []any{
		map[string]any{"type": "custom_tool_call", "id": "patch-item", "call_id": "patch-call", "name": applyPatchToolName, "input": patch, "status": "completed"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "patch-call", "output": original},
	})}}
	if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	parts := projectedEditOutput(t, &request, "patch-call")
	if len(parts) != 2 || parts[0]["text"] != original {
		t.Fatalf("native result changed: %+v", parts)
	}
	notice, _ := parts[1]["text"].(string)
	if !strings.Contains(notice, "Capture incomplete:") || !strings.Contains(notice, "bounded regular file") || strings.Contains(notice, "--summary") {
		t.Fatalf("missing capture warning: %q", notice)
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
			arguments := string(responses.MustMarshalJSON(map[string]any{"cmd": "printf 'new\\n' > file.txt"}))
			retainCommandObservation(t, transform, "shell-call", arguments)
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
			request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
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
	request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, []any{call, map[string]any{
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
	arguments := string(responses.MustMarshalJSON(map[string]any{"cmd": "printf 'new\\n' > file.txt"}))
	retainCommandObservation(t, transform, "shell-call", arguments)
	running := nativeExecOutput("Process running with session ID 9")
	items := []any{
		map[string]any{"type": "function_call", "id": "shell-call-item", "call_id": "shell-call", "name": nativeExecCommandToolName, "arguments": arguments},
		map[string]any{"type": "function_call_output", "call_id": "shell-call", "output": running},
	}
	request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
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
	request = codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
	if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	assertEditNotice(t, transform.ctx, proxy, workspace, &request, "stdin-call", "shell-call:exec:1", completed, "file.txt", true)
}

func TestAgentEditNoticeRetainsOnlyIDsForLargeCapture(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	var ids []string
	for index := range 2 {
		call := legacycall.PatchCallID("many-patches", index)
		id, err := testProxyStore(proxy).changes.Reserve(t.Context(), testProxyStore(proxy).scoped(t.Context()).changeScope(), workspace, "author", call)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		var files []diffmodel.ReviewFile
		for number := range 16 {
			path := fmt.Sprintf("%d-%d-%s", index, number, strings.Repeat("x", 600))
			files = append(files, diffmodel.ReviewFile{AfterPath: path, Diff: "add \"\" -> \"" + path + "\"\n"})
		}
		if err := testProxyStore(proxy).put(t.Context(), workspace, map[string]mekugiHistory{call: {
			ChangeID: id, CorrelationID: call, ReviewFiles: files,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	notice, err := testProxyStore(proxy).agentEditNotice(t.Context(), workspace, "many-patches", mekugiHistory{NativePatches: make([]changecapture.PatchObservation, 2)})
	if err != nil {
		t.Fatal(err)
	}
	if notice != strings.Join(ids, " ") {
		t.Fatalf("notice lost IDs or exceeded its bound: %q", notice)
	}
}

// Automatic receipts inspect metadata, never render the explicit summary view.
type noReceiptRead struct{ changes.Service }

func (noReceiptRead) Read(context.Context, changes.Scope, changes.Query) (string, error) {
	panic("automatic receipt rendered change details")
}

type failedReceiptInspect struct{ changes.Service }

func (failedReceiptInspect) Inspect(changes.Scope, string) (changes.Snapshot, error) {
	return changes.Snapshot{}, errors.New("capture index unavailable")
}

func TestAgentEditNoticeCaptureDiagnosticsWithoutStatistics(t *testing.T) {
	for _, scenario := range []string{"complete", "incomplete", "scope-only", "command-failed", "receipt-failure"} {
		t.Run(scenario, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			store := testProxyStore(proxy)
			workspace := t.TempDir()
			call := legacycall.ExecCallID("edit", false)
			record := mekugiHistory{ChangeID: "amber1", CorrelationID: "edit\x00exec",
				ReviewFiles: []diffmodel.ReviewFile{{AfterPath: "file", Diff: "add file\n+new\n"}},
				ExecOutcome: &changecapture.Outcome{Coverage: "exact", Status: "completed"}}
			switch scenario {
			case "incomplete":
				record.ReviewFiles[0].Incomplete = "baseline unavailable"
			case "scope-only":
				record.ChangeID, record.ReviewFiles = "", nil
				record.ExecOutcome.Coverage, record.ExecOutcome.ScopeReason = "partial", "workspace checkpoint unavailable"
			case "command-failed":
				record.ExecOutcome.Status, record.ExecOutcome.Exit = "failed", new(7)
			}
			if record.ChangeID != "" {
				id, err := store.changes.Reserve(t.Context(), store.changeScope(), workspace, "author", record.CorrelationID)
				if err != nil {
					t.Fatal(err)
				}
				record.ChangeID = id
				record.ExecutingThread = "author"
			}
			if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: record}); err != nil {
				t.Fatal(err)
			}
			var source changes.Service = noReceiptRead{store.changes}
			if scenario == "receipt-failure" {
				source = failedReceiptInspect{source}
			}
			notice, err := agentEditNotice(t.Context(), store.changeScope(), source, workspace, "edit", mekugiHistory{ExecObservation: &changecapture.Observation{}})
			if scenario == "receipt-failure" {
				if err == nil || !strings.Contains(err.Error(), "capture index unavailable") {
					t.Fatalf("missing capture failure: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := record.ChangeID
			if scenario == "incomplete" {
				want += "\nCapture incomplete: baseline unavailable"
			}
			if scenario == "scope-only" {
				want = "Capture incomplete: workspace checkpoint unavailable"
			}
			if notice != want {
				t.Fatalf("notice = %q, want %q", notice, want)
			}
		})
	}
}
