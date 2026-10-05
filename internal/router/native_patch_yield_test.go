package router

import (
	jsonv1 "encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodeModeCompletedPatchFinalizesBeforeSiblingYieldedCommand(t *testing.T) {
	t.Parallel()
	for _, exit := range []int{0, 1} {
		t.Run(fmt.Sprintf("command_exit_%d", exit), func(t *testing.T) {
			testCompletedPatchBeforeYieldedCommand(t, exit)
		})
	}
}

func testCompletedPatchBeforeYieldedCommand(t *testing.T, exit int) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	trace := newNativeTraceFixture(t)
	proxy.nativeTrace = &nativeToolTrace{directory: trace.root}
	transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
	transform.sessionShell = "bash"
	target := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	firstPatch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+middle\n*** End Patch\n"
	secondPatch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-middle\n+final\n*** End Patch\n"
	firstSource := "await tools.apply_patch(" + string(mustMarshalJSON(firstPatch)) + "); await tools.exec_command({cmd: 'printf command > command.txt'});"
	secondSource := "await tools.apply_patch(" + string(mustMarshalJSON(secondPatch)) + ");"
	codeCall := func(id, source string) map[string]any {
		return map[string]any{
			"type": "custom_tool_call", "id": id + "-item", "call_id": id,
			"name": "exec", "input": source, "status": "completed",
		}
	}
	codeOutput := func(id string) map[string]any {
		return map[string]any{"type": "custom_tool_call_output", "call_id": id, "output": []any{
			map[string]any{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"},
		}}
	}
	observe := func(call map[string]any) {
		t.Helper()
		if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
			"id": "response", "status": "completed", "output": []any{call},
		})); err != nil {
			t.Fatal(err)
		}
	}
	reconcile := func(items []any) {
		t.Helper()
		request := parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
		if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
			t.Fatal(err)
		}
	}
	lookup := func(id string) (mekugiHistory, bool) {
		t.Helper()
		history, found, err := proxy.replayStore.lookup(transform.ctx, workspace, id)
		if err != nil {
			t.Fatal(err)
		}
		return history, found
	}

	firstCall := codeCall("first-cell", firstSource)
	observe(firstCall)
	trace.start("thread-1", "runtime-1", "first-cell", firstSource)
	trace.tool("thread-1", "runtime-1", "first-patch", "apply_patch", firstPatch)
	trace.result("thread-1", "first-patch", "completed", map[string]any{})
	trace.tool("thread-1", "runtime-1", "running-command", "exec_command", `{"cmd":"printf command > command.txt"}`)
	trace.result("thread-1", "running-command", "completed", map[string]any{"session_id": 123})
	trace.end("thread-1", "runtime-1")
	if err := os.WriteFile(target, []byte("middle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	items := []any{firstCall, codeOutput("first-cell")}
	reconcile(items)
	first, found := lookup(nativePatchDerivedCallID("first-cell", 0))
	if !found || first.ChangeID == "" || len(first.ReviewFiles) != 1 || first.TranslationError != "" {
		t.Fatalf("completed patch delayed by sibling process: found=%v evidence=%+v", found, first)
	}
	if _, found := lookup(execDerivedCallID("first-cell", true)); found {
		t.Fatal("yielded command captured before its process exited")
	}
	outer, found := lookup("first-cell")
	if !found {
		t.Fatal("exec call missing while command is pending")
	}
	notice, err := proxy.replayStore.agentEditNotice(transform.ctx, workspace, "first-cell", outer)
	if err != nil || !strings.Contains(notice, first.ChangeID) {
		t.Fatalf("completed patch has no immediate edit notice: notice=%q err=%v", notice, err)
	}
	firstDiff, err := proxy.replayStore.readChanges(transform.ctx, changeReadOptions{workspace: workspace, ids: []string{first.ChangeID}, maxTokens: 4000})
	if err != nil || !strings.Contains(firstDiff, "-old") || !strings.Contains(firstDiff, "+middle") || strings.Contains(firstDiff, "+final") {
		t.Fatalf("first capture before next edit = %q, %v", firstDiff, err)
	}

	secondCall := codeCall("second-cell", secondSource)
	// Each model response has its own transform and history commit.
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, items...),
		"tools": []any{map[string]any{"type": "function", "name": "lookup"}}, "tool_choice": "auto",
	}))
	if err != nil {
		t.Fatal(err)
	}
	transform, err = proxy.prepareRequest(t.Context(), &request, "session-1", "thread-1", codexTurnMetadata{
		RequestKind: "turn", Directories: map[string]jsonv1.RawMessage{workspace: nil},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	observe(secondCall)
	trace.start("thread-1", "runtime-2", "second-cell", secondSource)
	trace.tool("thread-1", "runtime-2", "second-patch", "apply_patch", secondPatch)
	trace.result("thread-1", "second-patch", "completed", map[string]any{})
	trace.end("thread-1", "runtime-2")
	if err := os.WriteFile(target, []byte("final\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	items = append(items, secondCall, codeOutput("second-cell"))
	reconcile(items)
	second, found := lookup(nativePatchDerivedCallID("second-cell", 0))
	if !found || second.ChangeID == "" || second.ChangeID == first.ChangeID {
		t.Fatalf("second patch evidence missing: found=%v evidence=%+v", found, second)
	}
	firstAgain, found := lookup(nativePatchDerivedCallID("first-cell", 0))
	if !found || firstAgain.ChangeID != first.ChangeID {
		t.Fatalf("first patch changed after second edit: found=%v evidence=%+v", found, firstAgain)
	}
	firstDiffAgain, err := proxy.replayStore.readChanges(transform.ctx, changeReadOptions{workspace: workspace, ids: []string{first.ChangeID}, maxTokens: 4000})
	if err != nil || firstDiffAgain != firstDiff {
		t.Fatalf("first patch snapshot drifted: before=%q after=%q err=%v", firstDiff, firstDiffAgain, err)
	}
	assertNet := func() {
		t.Helper()
		net, err := proxy.replayStore.readChanges(transform.ctx, changeReadOptions{
			workspace: workspace, ids: []string{first.ChangeID, second.ChangeID}, view: "net", maxTokens: 4000,
		})
		if err != nil || !strings.Contains(net, "-old") || !strings.Contains(net, "+final") || strings.Contains(net, "+middle") || strings.Contains(net, "-middle") {
			t.Fatalf("two patch captures did not compose: net=%q err=%v", net, err)
		}
	}
	assertNet()

	// The earlier process finishes only after both patches have been captured.
	// Replaying the first call must not rewrite its already-durable snapshot.
	trace.event("thread-1", map[string]any{
		"type": "tool_call_runtime_ended", "tool_call_id": "running-command", "status": "completed",
		"runtime_payload": trace.ref(map[string]any{"exit_code": exit}),
	})
	if err := os.WriteFile(filepath.Join(workspace, "command.txt"), []byte("command"), 0o600); err != nil {
		t.Fatal(err)
	}
	items = append(items, map[string]any{"role": "user", "content": "continue"})
	reconcile(items)
	command, found := lookup(execDerivedCallID("first-cell", true))
	wantStatus := execStatusCompleted
	if exit != 0 {
		wantStatus = execStatusFailed
	}
	if !found || command.ExecOutcome == nil || command.ExecOutcome.Status != wantStatus || command.ChangeID == "" ||
		len(command.ReviewFiles) != 1 || command.ReviewFiles[0].AfterPath != filepath.Join(workspace, "command.txt") {
		t.Fatalf("terminal command did not finalize: found=%v evidence=%+v", found, command)
	}
	firstLast, found := lookup(nativePatchDerivedCallID("first-cell", 0))
	if !found || firstLast.ChangeID != first.ChangeID {
		t.Fatalf("first patch changed after command exit: found=%v evidence=%+v", found, firstLast)
	}
	firstDiffLast, err := proxy.replayStore.readChanges(transform.ctx, changeReadOptions{workspace: workspace, ids: []string{first.ChangeID}, maxTokens: 4000})
	if err != nil || firstDiffLast != firstDiff {
		t.Fatalf("late command completion rewrote first patch: before=%q after=%q err=%v", firstDiff, firstDiffLast, err)
	}
	assertNet()
}
