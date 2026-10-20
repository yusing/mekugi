package executionobservation

import (
	jsonv1 "encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/nativetrace"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestYieldedNamedWriterPatchIntervals(t *testing.T) {
	for _, scenario := range []string{"completed", "failed", "no-continuation", "missing-boundary", "missing-prefix", "no-effect"} {
		t.Run(scenario, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			trace := newNativeTraceFixture(t)
			proxy.nativeTrace = nativetrace.New(trace.root)
			transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
			transform.sessionShell = "bash"
			path := filepath.Join(workspace, "file.txt")
			initial := "A\n"
			if scenario == "no-effect" {
				initial = "B\n"
			}
			writeTestFile(t, path, initial)
			call := func(id, source string) map[string]any {
				return map[string]any{"type": "custom_tool_call", "id": id + "-item", "call_id": id, "name": "exec", "input": source, "status": "completed"}
			}
			output := func(id string) map[string]any {
				return map[string]any{"type": "custom_tool_call_output", "call_id": id, "output": []any{map[string]any{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"}}}
			}
			observe := func(value map[string]any) {
				t.Helper()
				if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "response", "status": "completed", "output": []any{value}})); err != nil {
					t.Fatal(err)
				}
			}
			var items []any
			reconcile := func(p *Preparer) {
				t.Helper()
				request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
				if _, err := p.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
					t.Fatal(err)
				}
			}
			lookup := func(id string) mekugiHistory {
				t.Helper()
				history, found, err := testProxyStore(proxy).lookup(transform.ctx, workspace, id)
				if err != nil || !found {
					t.Fatalf("lookup %s: found=%v, %v", id, found, err)
				}
				return history
			}
			writerSource := `await tools.exec_command({cmd: "printf 'B\\n' > file.txt"});`
			writer := call("writer", writerSource)
			observe(writer)
			trace.start("thread-1", "writer-runtime", "writer", writerSource)
			trace.tool("thread-1", "writer-runtime", "writer-command", "exec_command", `{"cmd":"printf 'B\\n' > file.txt"}`)
			trace.result("thread-1", "writer-command", "completed", map[string]any{"session_id": 101})
			trace.end("thread-1", "writer-runtime")
			writeTestFile(t, path, "B\n")
			items = []any{writer, output("writer")}
			reconcile(proxy)

			request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
				"model": "gpt-test", "input": append([]any{testCodeModeAdditionalTools(testCodeModeDescription)}, items...),
				"tools": []any{map[string]any{"type": "function", "name": "lookup"}}, "tool_choice": "auto",
			}))
			if err != nil {
				t.Fatal(err)
			}
			transform, err = proxy.prepareRequest(t.Context(), &request, "session-1", "thread-1", codexresponses.TurnMetadata{RequestKind: "turn", Directories: map[string]jsonv1.RawMessage{workspace: nil}}, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(transform.Close)
			patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-B\n+C\n*** End Patch\n"
			patchSource := "await tools.apply_patch(" + string(responses.MustMarshalJSON(patch)) + ");"
			patchCall := call("patch", patchSource)
			observe(patchCall)
			store := testProxyStore(proxy)
			prefixID := legacycall.ExecCallID("writer", true) + ":interval:0"
			prefix, boundaryFound, err := store.lookup(transform.ctx, workspace, prefixID)
			if err != nil {
				t.Fatal(err)
			}
			query := changes.Query{Workspace: workspace, View: "net"}
			if boundaryFound {
				query.IDs = []string{prefix.ChangeID}
				if text, err := store.changes.Read(transform.ctx, store.scoped(transform.ctx).changeScope(), query); err == nil || !strings.Contains(err.Error(), "pending") || text != "" {
					t.Fatalf("writer interval completed the host: %q, %v", text, err)
				}
			}
			trace.start("thread-1", "patch-runtime", "patch", patchSource)
			trace.tool("thread-1", "patch-runtime", "patch-command", "apply_patch", patch)
			trace.result("thread-1", "patch-command", "completed", map[string]any{})
			trace.end("thread-1", "patch-runtime")
			writeTestFile(t, path, "C\n")
			items = append(items, patchCall, output("patch"))
			reconcile(proxy)
			patchHistory := lookup(legacycall.PatchCallID("patch", 0))
			if len(patchHistory.ReviewFiles) != 1 || !strings.Contains(patchHistory.ReviewFiles[0].Diff, "-B\n+C") || strings.Contains(patchHistory.ReviewFiles[0].Diff, "-A") {
				t.Fatalf("patch stole writer attribution: %+v", patchHistory)
			}
			if _, found, err := store.lookup(transform.ctx, workspace, legacycall.ExecCallID("writer", true)); found || err != nil {
				t.Fatalf("writer prematurely terminal: %v %v", found, err)
			}
			if scenario == "completed" {
				selected := store.scoped(transform.ctx).changeScope()
				child := selected
				child.Namespace, child.Thread = "interval-fork", "interval-fork"
				err := selected.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
					plan, err := store.changes.PrepareFork(child, selected.Namespace, child.Namespace, selected.Thread)
					if err != nil {
						return err
					}
					if err := tx.Retain(child.Thread, history.Limits{}, plan.Dependencies()...); err != nil {
						return err
					}
					return plan.Publish(tx)
				})
				if err != nil {
					t.Fatal(err)
				}
				reopened, err := openMekugiReplayStore(store.directory)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(reopened.snapshots.Close)
				child.Store = reopened.changeScope().Store
				intervals, err := readExecIntervals(t.Context(), child, reopened.changes, workspace, "writer", true)
				if err != nil || len(intervals) != 1 || intervals[0].after == nil {
					t.Fatalf("pending fork/resume intervals: %+v, %v", intervals, err)
				}
				query.IDs = []string{prefix.ChangeID}
				if _, err := reopened.changes.Read(t.Context(), child, query); err == nil || !strings.Contains(err.Error(), "pending") {
					t.Fatalf("fork revived unfinished writer: %v", err)
				}
				deps, err := reopened.changes.Dependencies(child, workspace, query.IDs, false)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{prefixID, prefixID + ":resume"} {
					found := false
					for _, name := range deps {
						found = found || name == legacycall.Name(workspace, id, false)
					}
					if !found {
						t.Fatalf("missing pending dependency %s: %v", id, deps)
					}
				}
			}
			if scenario == "missing-prefix" {
				if err := os.Remove(filepath.Join(store.directory, legacycall.Name(workspace, prefixID, false))); err != nil {
					t.Fatal(err)
				}
				request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": mustTestJSON(t, items)}}
				if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err == nil || !strings.Contains(err.Error(), "interval missing") {
					t.Fatalf("missing prefix silently lost: %v", err)
				}
				if _, found, err := store.lookup(transform.ctx, workspace, legacycall.ExecCallID("writer", true)); found || err != nil {
					t.Fatalf("missing prefix completed writer: %v, %v", found, err)
				}
				return
			}
			if scenario == "missing-boundary" && boundaryFound {
				if err := os.Remove(filepath.Join(store.directory, legacycall.Name(workspace, prefixID+":resume", false))); err != nil {
					t.Fatal(err)
				}
			}
			var pendingPath string
			var pendingIndex []byte
			if scenario == "no-effect" {
				deps, err := store.changes.Dependencies(store.scoped(transform.ctx).changeScope(), workspace, []string{prefix.ChangeID}, false)
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range deps {
					if strings.HasPrefix(name, "changes-v3-") {
						pendingPath = filepath.Join(store.directory, name)
						pendingIndex, err = os.ReadFile(pendingPath)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			last := "D"
			if scenario == "no-continuation" || scenario == "no-effect" {
				last = "C"
			}
			writeTestFile(t, path, last+"\n")
			exit := 0
			if scenario == "failed" {
				exit = 1
			}
			trace.event("thread-1", map[string]any{"type": "tool_call_runtime_ended", "tool_call_id": "writer-command", "status": "completed", "runtime_payload": trace.ref(map[string]any{"exit_code": exit})})
			reconcile(proxy)
			terminal := lookup(legacycall.ExecCallID("writer", true))
			if terminal.ExecOutcome == nil || terminal.ExecOutcome.Exit == nil || *terminal.ExecOutcome.Exit != exit {
				t.Fatalf("writer terminal identity/outcome: %+v", terminal)
			}
			if scenario == "no-effect" {
				if terminal.ChangeID != "" || len(terminal.ReviewFiles) != 0 {
					t.Fatalf("no-effect writer kept a change: %+v", terminal)
				}
				index, err := store.changes.Inspect(store.scoped(transform.ctx).changeScope(), workspace)
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := index.Changes[prefix.ChangeID]; exists {
					t.Fatal("no-effect reservation remained pending")
				}
				if notice, err := store.agentEditNotice(transform.ctx, workspace, "writer", lookup("writer")); err != nil || notice != "" {
					t.Fatalf("no-effect receipt: %q, %v", notice, err)
				}
				// Reproduce interruption after the terminal record but before index settlement.
				if err := os.WriteFile(pendingPath, pendingIndex, 0600); err != nil {
					t.Fatal(err)
				}
				reconcile(proxy)
				index, err = store.changes.Inspect(store.scoped(transform.ctx).changeScope(), workspace)
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := index.Changes[prefix.ChangeID]; exists {
					t.Fatal("replay did not settle no-effect reservation")
				}
				return
			}
			query.IDs = []string{terminal.ChangeID, patchHistory.ChangeID}
			assertEvidence := func(s *mekugiReplayStore) {
				t.Helper()
				net, err := s.changes.Read(transform.ctx, s.scoped(transform.ctx).changeScope(), query)
				if scenario == "missing-boundary" {
					if err == nil || !strings.Contains(err.Error(), "unavailable") && !strings.Contains(err.Error(), "partial") || !strings.Contains(net, "+C") {
						t.Fatalf("missing boundary invented a chain: %q, %v", net, err)
					}
				} else if err != nil || strings.Count(net, "-A\n+"+last) != 1 || strings.Contains(net, "+B") {
					t.Fatalf("net intervals: %q, %v", net, err)
				}
				var orders []uint64
				for _, id := range []string{prefixID, legacycall.PatchCallID("patch", 0), legacycall.ExecCallID("writer", true)} {
					record, found, err := s.read(workspace, id, false)
					if err != nil || !found {
						t.Fatalf("interval record %s: %v, %v", id, found, err)
					}
					orders = append(orders, record.CaptureOrder)
				}
				if orders[0] == 0 || orders[0] >= orders[1] || scenario != "no-continuation" && orders[1] >= orders[2] {
					t.Fatalf("interval capture order: %v", orders)
				}
				snapshot, err := s.liveDiffSnapshot(transform.ctx, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread-1": true}}})
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.Len() != 3 {
					t.Fatalf("duplicate/missing retained intervals: %d", snapshot.Len())
				}
				if receipts := snapshot.Receipts(); len(receipts) != 1 || !strings.Contains(receipts[0].Text, "-A\n+B") || scenario != "missing-boundary" && scenario != "no-continuation" && !strings.Contains(receipts[0].Text, "-C\n+D") {
					t.Fatalf("writer receipt duplicated or lost an interval: %+v", receipts)
				}
				writerDetail := snapshot.ActivityDetail(activitydetail.Request{Operation: activitydetail.Operation{Thread: "thread-1", Item: "writer-command"}})
				wantPages := 2
				if scenario == "no-continuation" {
					wantPages = 1
				}
				if len(writerDetail.Pages) != wantPages {
					t.Fatalf("writer detail lost intervals: %+v", writerDetail)
				}
				var details strings.Builder
				for _, page := range writerDetail.Pages {
					details.WriteString(page.Code)
				}
				if !strings.Contains(details.String(), "-A\n+B") || strings.Contains(details.String(), "-B\n+C") || scenario != "missing-boundary" && scenario != "no-continuation" && !strings.Contains(details.String(), "-C\n+D") {
					t.Fatalf("writer detail includes another operation: %s", details.String())
				}
				patchDetail := snapshot.ActivityDetail(activitydetail.Request{Operation: activitydetail.Operation{Thread: "thread-1", Item: "patch"}})
				if len(patchDetail.Pages) != 1 || !strings.Contains(patchDetail.Pages[0].Code, "-B\n+C") {
					t.Fatalf("patch detail: %+v", patchDetail)
				}
			}
			assertEvidence(store)
			if !boundaryFound || terminal.ChangeID != prefix.ChangeID || prefix.ExecOutcome != nil || len(prefix.ReviewFiles) != 1 || !strings.Contains(prefix.ReviewFiles[0].Diff, "-A\n+B") {
				t.Fatalf("prefix identity/attribution: %+v", prefix)
			}
			if scenario != "missing-boundary" && scenario != "no-continuation" && (len(terminal.ReviewFiles) != 1 || !strings.Contains(terminal.ReviewFiles[0].Diff, "-C\n+D")) {
				t.Fatalf("continuation attribution: %+v", terminal)
			}
			if notice, err := store.agentEditNotice(transform.ctx, workspace, "writer", lookup("writer")); err != nil || !strings.Contains(notice, prefix.ChangeID) {
				t.Fatalf("writer notice lost intervals: %q, %v", notice, err)
			}
			// The final snapshot must survive loss of process/window/trace state.
			resumed := newManagedMekugiProxy(t)
			reopened, err := openMekugiReplayStore(store.directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reopened.snapshots.Close)
			setTestProxyStore(resumed, reopened)
			writeTestFile(t, path, "unrelated live bytes\n")
			reconcile(resumed)
			assertEvidence(reopened)
			if data, err := os.ReadFile(path); err != nil || string(data) != "unrelated live bytes\n" {
				t.Fatalf("replay repeated an effect: %q, %v", data, err)
			}
			if scenario == "failed" && terminal.ExecOutcome.Status != changecapture.StatusFailed {
				t.Fatal("failed writer was reported successful")
			}
		})
	}
}
