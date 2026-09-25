package router

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExecExplicitSiblingsShareDurableRecord(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	transform := prepareNativeStockTransform(t, proxy, workspace, "siblings")
	var items []any
	for _, ref := range []string{"first", "second"} {
		arguments := string(mustMarshalJSON(map[string]any{"cmd": "printf " + ref + " > generated", "workdir": workspace}))
		streamNativeExecCommand(t, transform, ref, arguments)
		items = append(items, map[string]any{"type": "function_call", "call_id": ref, "name": nativeExecCommandToolName, "arguments": arguments},
			map[string]any{"type": "function_call_output", "call_id": ref, "output": nativeExecOutput("Process exited with code 0")})
	}
	writeTestFile(t, filepath.Join(workspace, "generated"), "generated\n")
	reconcileExecItems(t, proxy, workspace, items)
	first, found, err := proxy.replayStore.lookup(t.Context(), workspace, "first:exec:1")
	if err != nil || !found || first.ChangeID == "" || first.ExecOutcome.Coverage != execCoverageExact {
		t.Fatalf("shared record: %+v %v", first, err)
	}
	if len(first.ReviewFiles) != 1 || !strings.Contains(first.Script, "printf second > generated") {
		t.Fatalf("missing merged evidence: %+v", first)
	}
	second, found, err := proxy.replayStore.lookup(t.Context(), workspace, "second:exec:1")
	if err != nil || !found || second.ChangeID != "" || second.ExecOutcome.SharedWith != "first:exec:1" {
		t.Fatalf("sibling linkage: %+v %v", second, err)
	}
	// Replay does not recapture or demand the complete former sibling set.
	writeTestFile(t, filepath.Join(workspace, "unrelated"), "later\n")
	reconcileExecItems(t, proxy, workspace, items)
	reconcileExecItems(t, proxy, workspace, items[2:])
	again, _, _ := proxy.replayStore.lookup(t.Context(), workspace, "first:exec:1")
	if len(again.ReviewFiles) != 1 || again.ChangeID != first.ChangeID {
		t.Fatal("replay changed retained evidence")
	}
}

func TestExecYieldedSiblingFinishesAfterOtherMemberWasSaved(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	transform := prepareNativeStockTransform(t, proxy, workspace, "yielded-siblings")
	firstArguments := string(mustMarshalJSON(map[string]any{"cmd": "printf first > first.txt", "workdir": workspace}))
	secondArguments := string(mustMarshalJSON(map[string]any{"cmd": "printf second > second.txt", "workdir": workspace, "yield_time_ms": 1000}))
	streamNativeExecCommand(t, transform, "first", firstArguments)
	streamNativeExecCommand(t, transform, "second", secondArguments)
	firstCall := map[string]any{"type": "function_call", "call_id": "first", "name": nativeExecCommandToolName, "arguments": firstArguments}
	secondCall := map[string]any{"type": "function_call", "call_id": "second", "name": nativeExecCommandToolName, "arguments": secondArguments}
	items := []any{
		firstCall,
		map[string]any{"type": "function_call_output", "call_id": "first", "output": nativeExecOutput("Process exited with code 0")},
		secondCall,
		map[string]any{"type": "function_call_output", "call_id": "second", "output": nativeExecOutput("Process running with session ID 9")},
	}
	writeTestFile(t, filepath.Join(workspace, "first.txt"), "first")
	reconcileExecItems(t, proxy, workspace, items)
	first, found, err := proxy.replayStore.lookup(t.Context(), workspace, "first:exec:1")
	if err != nil || !found || first.ChangeID == "" || len(first.ReviewFiles) != 1 {
		t.Fatalf("first terminal member was not saved: %+v found=%v err=%v", first, found, err)
	}
	if _, found, err := proxy.replayStore.lookup(t.Context(), workspace, "second:exec:1"); err != nil || found {
		t.Fatalf("yielded member finalized before host completion: found=%v err=%v", found, err)
	}
	writeTestFile(t, filepath.Join(workspace, "second.txt"), "second")
	items = append(items,
		map[string]any{"type": "function_call", "call_id": "stdin-second", "name": "write_stdin", "arguments": `{"session_id":9,"chars":""}`},
		map[string]any{"type": "function_call_output", "call_id": "stdin-second", "output": nativeExecOutput("Process exited with code 0")},
	)
	reconcileExecItems(t, proxy, workspace, items)
	second, found, err := proxy.replayStore.lookup(t.Context(), workspace, "second:exec:1")
	if err != nil || !found || second.ChangeID == "" || second.ChangeID == first.ChangeID || len(second.ReviewFiles) != 1 ||
		!strings.Contains(second.ReviewFiles[0].Diff, "+second") {
		t.Fatalf("later terminal member lost its own saved edit: %+v found=%v err=%v", second, found, err)
	}
	writeTestFile(t, filepath.Join(workspace, "unrelated.txt"), "later")
	reconcileExecItems(t, proxy, workspace, items)
	reconcileExecItems(t, proxy, workspace, items[2:])
	firstAgain, _, _ := proxy.replayStore.lookup(t.Context(), workspace, "first:exec:1")
	secondAgain, _, _ := proxy.replayStore.lookup(t.Context(), workspace, "second:exec:1")
	if firstAgain.ChangeID != first.ChangeID || secondAgain.ChangeID != second.ChangeID || len(secondAgain.ReviewFiles) != 1 {
		t.Fatalf("replay recaptured or reallocated sibling edits: first=%+v second=%+v", firstAgain, secondAgain)
	}
}
