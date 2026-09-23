package router

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExecSweptSiblingsShareDurableRecord(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	transform := prepareNativeStockTransform(t, proxy, workspace, "siblings")
	var items []any
	for _, ref := range []string{"first", "second"} {
		arguments := string(mustMarshalJSON(map[string]any{"cmd": "make " + ref, "workdir": workspace}))
		streamNativeExecCommand(t, transform, ref, arguments)
		items = append(items, map[string]any{"type": "function_call", "call_id": ref, "name": nativeExecCommandToolName, "arguments": arguments},
			map[string]any{"type": "function_call_output", "call_id": ref, "output": nativeExecOutput("Process exited with code 0")})
	}
	writeTestFile(t, filepath.Join(workspace, "generated"), "generated\n")
	reconcileExecItems(t, proxy, workspace, items)
	first, found, err := proxy.replayStore.lookup(t.Context(), workspace, "first:exec:1")
	if err != nil || !found || first.ChangeID == "" || first.Applied || first.ExecOutcome.Coverage != execCoveragePartial {
		t.Fatalf("shared record: %+v %v", first, err)
	}
	if len(first.ReviewFiles) != 1 || first.ReviewFiles[0].Origin == "" || !strings.Contains(first.Script, "make second") {
		t.Fatalf("missing merged evidence: %+v", first)
	}
	second, found, err := proxy.replayStore.lookup(t.Context(), workspace, "second:exec:1")
	if err != nil || !found || second.ChangeID != "" || second.ExecOutcome.SharedWith != "first:exec:1" {
		t.Fatalf("sibling linkage: %+v %v", second, err)
	}
	// Replay neither sweeps again nor demands the complete former sibling set.
	writeTestFile(t, filepath.Join(workspace, "unrelated"), "later\n")
	reconcileExecItems(t, proxy, workspace, items)
	reconcileExecItems(t, proxy, workspace, items[2:])
	again, _, _ := proxy.replayStore.lookup(t.Context(), workspace, "first:exec:1")
	if len(again.ReviewFiles) != 1 || again.ChangeID != first.ChangeID {
		t.Fatal("replay changed retained evidence")
	}
}
