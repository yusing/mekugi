package executionobservation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

type completionReads struct {
	changes.Service
	reads atomic.Int32
}

func TestParentFinalCaptureAfterLabeledWorkerResult(t *testing.T) {
	f := newMChangesSliceFixture(t, "capture-parent")
	bindTestHandleScope(t, f.store, f.ctx, "", "")
	p := newManagedMekugiProxy(t)
	setTestProxyStore(p, f.store)
	child := f.thread + "-worker"
	directory := filepath.Join(f.workspace, "native")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := p.AdmitNativeChild(f.ctx, appserver.ChildHostAdmissionRequest{RootThreadID: f.thread, ParentThreadID: f.thread, ThreadID: child, LocalCwd: &directory}); err != nil {
		t.Fatal(err)
	}
	model, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]any{"role": "user", "content": "edit"}}}))
	if err != nil {
		t.Fatal(err)
	}
	transform, err := p.prepareRequest(t.Context(), &model, child, child, codexresponses.TurnMetadata{RequestKind: "turn", ThreadID: child, ParentThreadID: f.thread, SubagentKind: "thread_spawn", Directories: map[string]json.RawMessage{directory: nil}}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer transform.Close()
	transform.sessionShell = "bash"
	call := map[string]any{"type": "custom_tool_call", "id": "edit-item", "call_id": "edit", "name": "exec", "input": `text(await tools.exec_command({cmd:"echo after > file.txt"}));`, "status": "completed"}
	if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "r", "status": "completed", "output": []any{call}})); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(directory, "file.txt"), "after\n")
	parts := []map[string]string{{"type": "input_text", "text": "[call c1]\nScript completed\nWall time 0.1 seconds\nOutput:\n"}, {"type": "input_text", "text": strings.Repeat("long output\n", 32)}}
	request := codexresponses.Request{Fields: map[string]json.RawMessage{"input": mustTestJSON(t, []any{call, map[string]any{"type": "custom_tool_call_output", "call_id": "edit", "output": parts}})}}
	if _, err := p.reconcileVisibleInput(transform.ctx, &request, directory, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	output := projectedEditOutput(t, &request, "edit")
	if len(output) != 3 || output[0]["text"] != parts[0]["text"] || output[1]["text"] != parts[1]["text"] {
		t.Fatalf("missing receipt after original result: %+v", output)
	}
	id := output[2]["text"].(string)
	result, err := p.PrepareNativeFinalCapture(t.Context(), appserver.FinalCaptureRequest{RootThreadID: f.thread, ThreadID: child, RecipientThreadID: f.thread, RecipientCwd: &f.workspace, OperationID: "work", Sources: []string{"mchanges:" + id}})
	if err != nil || result == nil || result.ResourceRef == "" || !strings.Contains(result.FullText, "+after\n") || strings.Contains(result.FullText, "exit_code=1") {
		t.Fatalf("parent did not receive published edit: %+v, %v", result, err)
	}
}

func (reader *completionReads) Read(ctx context.Context, selected changes.Scope, query changes.Query) (string, error) {
	reader.reads.Add(1)
	return reader.Service.Read(ctx, selected, query)
}

func (reader *completionReads) Freeze(ctx context.Context, selected changes.Scope, query changes.Query) (changes.FrozenRead, error) {
	reader.reads.Add(1)
	return reader.Service.Freeze(ctx, selected, query)
}

func TestNativeFinalCaptureUsesRecipientStorageWithoutGoCoordination(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-final")
	id := f.reserve(t, f.thread, "edit")
	f.publish(t, id, "edit", "edit-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("file.txt", "file.txt", "before\n", "after\n")}})
	p := testProxyWithStore(f.store)
	reader := &completionReads{Service: p.Changes}
	p.Changes = reader
	input := appserver.FinalCaptureRequest{RootThreadID: f.thread, ThreadID: "child", OperationID: "work", RecipientThreadID: f.thread, RecipientCwd: &f.workspace, Sources: []string{"mchanges:" + id}}
	body, err := p.PrepareNativeFinalCapture(t.Context(), input)
	if err != nil || body == nil || body.ResourceRef == "" || !strings.Contains(body.FullText, "+after") || strings.Contains(body.FullText, "private.txt") || reader.reads.Load() != 1 {
		t.Fatalf("native capture = %v, %v, reads %d", body, err, reader.reads.Load())
	}
	input.Sources = nil
	if body, err := p.PrepareNativeFinalCapture(t.Context(), input); err != nil || body != nil || reader.reads.Load() != 1 {
		t.Fatalf("unselected final acquired sources: %v, %v", body, err)
	}
	input.Sources = []string{"mchanges:amber1.."}
	if body, err := p.PrepareNativeFinalCapture(t.Context(), input); err != nil || body == nil || !strings.Contains(body.FullText, "CONTENT NOT ATTACHED") {
		t.Fatalf("malformed final lost capture diagnostic: %v, %v", body, err)
	}
	input.Sources = []string{"mchanges:" + id}
	input.RecipientCwd = nil
	if body, err := p.PrepareNativeFinalCapture(t.Context(), input); err != nil || body == nil || !strings.Contains(body.FullText, "workspace is unavailable") || reader.reads.Load() != 1 {
		t.Fatalf("unavailable recipient cwd fell back to another workspace: %v, %v", body, err)
	}
	input.RecipientCwd = &f.workspace
	if body, err := new(Preparer).PrepareNativeFinalCapture(t.Context(), input); err != nil || body == nil || !strings.Contains(body.FullText, "storage is unavailable") {
		t.Fatalf("missing storage discarded final: %v, %v", body, err)
	}
	input.RecipientThreadID = "unadmitted-recipient"
	if body, err := p.PrepareNativeFinalCapture(t.Context(), input); err != nil || body == nil || !strings.Contains(body.FullText, "scope is not admitted") || reader.reads.Load() != 1 {
		t.Fatalf("unadmitted recipient used another namespace: %v, %v", body, err)
	}
	if _, found, err := p.Retained.Store.ReadScope(input.RecipientThreadID); found || err != nil {
		t.Fatalf("failed recipient read admitted storage: found=%v err=%v", found, err)
	}
}
