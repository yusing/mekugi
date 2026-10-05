package router

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func prepareNativeStockTransform(t *testing.T, proxy *mekugiProxy, workspace, session string) *mekugiResponseTransform {
	t.Helper()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{
				"type": "input_text", "text": "<environment_context>\n  <cwd>" + workspace + "</cwd>\n  <shell>bash</shell>\n</environment_context>",
			}}},
			map[string]any{"role": "user", "content": "task"},
		},
		"tools": testExecResponsesTools(), "tool_choice": "auto",
	}))
	if err != nil {
		t.Fatal(err)
	}
	transform, err := proxy.prepareRequest(t.Context(), &request, session, "stock-thread", codexTurnMetadata{
		RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	return transform
}

// Seed the saved baseline of an older patch without reviving its execution.
func retainPatchObservation(t *testing.T, transform *mekugiResponseTransform, patch string) {
	t.Helper()
	patches := nativePatchesInCall(applyPatchToolName, patch, transform.directory)
	transform.openExecWindow("patch-call", nil, patches)
	item := map[string]json.RawMessage{"type": mustMarshalJSON("custom_tool_call"), "call_id": mustMarshalJSON("patch-call"), "name": mustMarshalJSON(applyPatchToolName), "input": mustMarshalJSON(patch)}
	transform.recordLocal("patch-call", &mekugiHistory{ToolName: applyPatchToolName, Script: patch, CarrierKind: codeModeCarrierCustom, CarrierName: applyPatchToolName, CarrierPayload: patch, ReplayCarrier: true, UpstreamItem: item, NativePatches: patches, ExecutingThread: transform.shellThreadID, Caller: transform.operationCaller()})
	if err := transform.commitLocalCall("patch-call"); err != nil {
		t.Fatal(err)
	}
}

func reconcileNativePatchResult(t *testing.T, proxy *mekugiProxy, workspace, patch, output string) mekugiHistory {
	t.Helper()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "tools": testExecResponsesTools(), "tool_choice": "auto",
		"input": []any{
			map[string]any{
				"type": "custom_tool_call", "id": "patch-item", "call_id": "patch-call",
				"name": applyPatchToolName, "input": patch, "status": "completed",
			},
			map[string]any{"type": "custom_tool_call_output", "call_id": "patch-call", "output": output},
			map[string]any{"role": "user", "content": "continue"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	transform, err := proxy.prepareRequest(t.Context(), &request, "stock-session-next", "stock-thread", codexTurnMetadata{
		RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	history, found, err := proxy.replayStore.lookup(transform.ctx, workspace, nativePatchDerivedCallID("patch-call", 0))
	if err != nil || !found {
		t.Fatalf("derived stock edit missing: found=%v err=%v", found, err)
	}
	return history
}

func TestNativeApplyPatchFailureNeverPublishesSuccessAndRetainsPartialOutcome(t *testing.T) {
	for _, test := range []struct {
		name, after string
		wantReview  bool
	}{
		{name: "unchanged", after: "old\n"},
		{name: "partial", after: "partial\n", wantReview: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			workspace := t.TempDir()
			target := filepath.Join(workspace, "file.txt")
			if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch\n"
			transform := prepareNativeStockTransform(t, proxy, workspace, "stock-failure-session")
			retainPatchObservation(t, transform, patch)
			if err := os.WriteFile(target, []byte(test.after), 0o600); err != nil {
				t.Fatal(err)
			}
			history := reconcileNativePatchResult(t, proxy, workspace, patch, "Error: patch failed")
			if history.TranslationError == "" || (len(history.ReviewFiles) != 0) != test.wantReview {
				t.Fatalf("failed evidence = %+v", history)
			}
			if len(history.CommentaryMessageIDs) != 0 {
				t.Fatalf("failure published success commentary: %+v", history.CommentaryMessageIDs)
			}
		})
	}
}

func TestNativePatchPathsIgnoresHunkContentThatLooksLikeAnOperation(t *testing.T) {
	workspace := t.TempDir()
	patch := "*** Begin Patch\n*** Update File: real.txt\n@@\n-*** Add File: fake.txt\n+replacement\n*** End Patch\n"
	paths, err := nativePatchPaths(patch, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0].before != filepath.Join(workspace, "real.txt") || paths[0].after != paths[0].before {
		t.Fatalf("observed paths = %+v", paths)
	}
}

func TestCodeModeLiteralPatchObservationInOrdinaryJavaScript(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: nested.txt\n+content\n*** End Patch\n"
	source := "const result = await Promise.allSettled([tools.exec_command({cmd: 'printf ready'}), tools.apply_patch(" + string(mustMarshalJSON(patch)) + ")]); text(result);"
	observed := nativePatchesInCall("exec", source, t.TempDir())
	if len(observed) != 1 || observed[0].Input != patch || len(observed[0].Files) != 1 {
		t.Fatalf("ordinary exec patch observation = %+v", observed)
	}
	if got := stockLiteralPatchInputs("const patch = " + string(mustMarshalJSON(patch)) + "; await tools.apply_patch(patch);"); len(got) != 1 || got[0] != patch {
		t.Fatalf("immutable literal binding was not recognized: %+v", got)
	}
	for _, static := range []string{
		"const\npatch = " + string(mustMarshalJSON(patch)) + "; await tools.apply_patch(patch);",
		"const patch = " + string(mustMarshalJSON(patch)) + ", other = 1; await tools.apply_patch(patch);",
		"const patch = " + string(mustMarshalJSON(patch)) + "; const result = await tools.apply_patch(patch); text(result);",
	} {
		if got := stockLiteralPatchInputs(static); len(got) != 1 || got[0] != patch {
			t.Fatalf("immutable literal binding variant was not recognized: %+v", got)
		}
	}
	for _, dynamic := range []string{
		"let patch = " + string(mustMarshalJSON(patch)) + "; await tools.apply_patch(patch);",
		"const patch = getPatch(); await tools.apply_patch(patch);",
		"const patch = " + string(mustMarshalJSON(patch)) + "; function run(patch) { return tools.apply_patch(patch); }",
		"const patch = " + string(mustMarshalJSON(patch)) + "; (() => tools.apply_patch(patch))();",
	} {
		if got := stockLiteralPatchInputs(dynamic); len(got) != 0 {
			t.Fatalf("dynamic argument was treated as literal: %+v", got)
		}
	}
}

func TestCodeModePatchNeedsTerminalResultAndNeverClaimsNestedSuccess(t *testing.T) {
	for _, test := range []struct {
		name    string
		yielded bool
		change  bool
	}{
		{name: "completed with effect", change: true},
		{name: "caught nested failure"},
		{name: "yielded then completed", yielded: true, change: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
			target := filepath.Join(workspace, "file.txt")
			if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch\n"
			call := map[string]any{
				"type": "custom_tool_call", "id": "code-item", "call_id": "code-call",
				"name": "exec", "input": "const patch = " + string(mustMarshalJSON(patch)) + "; text(await tools.apply_patch(patch));", "status": "completed",
			}
			if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
				"id": "response", "status": "completed", "output": []any{call},
			})); err != nil {
				t.Fatal(err)
			}
			codeOutput := func(status string) map[string]any {
				return map[string]any{"type": "custom_tool_call_output", "call_id": "code-call", "output": []any{
					map[string]any{"type": "input_text", "text": status + "\nWall time 0.1 seconds\nOutput:\n"},
				}}
			}
			reconcile := func(items []any) {
				t.Helper()
				request := parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustTestJSON(t, items)}}
				if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
					t.Fatal(err)
				}
			}
			items := []any{call}
			if test.yielded {
				items = append(items, codeOutput("Script running with cell ID cell-7"))
				if terminal, _, output, cell := stockPatchResultState("exec", mustTestJSON(t, items[1].(map[string]any)["output"])); terminal || cell != "cell-7" {
					t.Fatalf("running state = terminal %v, cell %q, output %q", terminal, cell, output)
				}
				reconcile(items)
				if _, found, err := proxy.replayStore.lookup(transform.ctx, workspace, nativePatchDerivedCallID("code-call", 0)); err != nil || found {
					t.Fatalf("yielded patch was prematurely completed: found=%v err=%v", found, err)
				}
				items = append(items,
					map[string]any{"type": "function_call", "call_id": "wait-call", "name": "wait", "arguments": `{"cell_id":"cell-7"}`},
					map[string]any{"type": "function_call_output", "call_id": "wait-call", "output": []any{
						map[string]any{"type": "input_text", "text": "Script completed\nWall time 0.2 seconds\nOutput:\n"},
					}},
				)
				if terminal, _, output, _ := stockPatchResultState("exec", mustTestJSON(t, items[len(items)-1].(map[string]any)["output"])); !terminal {
					t.Fatalf("wait state = terminal %v, output %q", terminal, output)
				}
			} else {
				items = append(items, codeOutput("Script completed"))
			}
			if test.change {
				if err := os.WriteFile(target, []byte("new\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			proxy.activity = newSubagentActivity()
			proxy.activity.observe("root", "", "/root", false)
			proxy.activity.observe("thread-1", "root", "/root/editor", true)
			reconcile(items)
			history, found, err := proxy.replayStore.lookup(transform.ctx, workspace, nativePatchDerivedCallID("code-call", 0))
			if err != nil || !found {
				t.Fatalf("completed patch missing: found=%v err=%v", found, err)
			}
			if history.AlreadySatisfied || history.TranslationError != "" ||
				(len(history.ReviewFiles) != 0) != test.change {
				t.Fatalf("completed patch = %+v", history)
			}
			listed, err := proxy.replayStore.readChanges(transform.ctx, changeReadOptions{workspace: workspace, view: "list"})
			want := ""
			if test.change {
				want = history.ChangeID + " +1 -1\n"
			} else if history.ChangeID != "" {
				t.Fatalf("unchanged attempt allocated a change ID: %s", history.ChangeID)
			}
			if err != nil || listed != want {
				t.Fatalf("mchanges list = %q, %v; want %q", listed, err, history.ChangeID)
			}
		})
	}
}

func TestCodeModePatchWindowRecordsSharedPathsOnce(t *testing.T) {
	for _, test := range []struct {
		name, first, second, finalPath string
	}{
		{"updates", "*** Update File: file.txt\n@@\n-old\n+middle", "*** Update File: file.txt\n@@\n-middle\n+final", "file.txt"},
		{"move then update", "*** Update File: file.txt\n*** Move to: moved.txt\n@@\n-old\n+middle", "*** Update File: moved.txt\n@@\n-middle\n+final", "moved.txt"},
		{"update then move", "*** Update File: file.txt\n@@\n-old\n+middle", "*** Update File: file.txt\n*** Move to: moved.txt\n@@\n-middle\n+final", "moved.txt"},
		{"delete then recreate", "*** Delete File: file.txt", "*** Add File: file.txt\n+final", "file.txt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			workspace := t.TempDir()
			target := filepath.Join(workspace, "file.txt")
			if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var source strings.Builder
			for _, body := range []string{test.first, test.second} {
				fmt.Fprintf(&source, "text(await tools.apply_patch(%s));\n", mustMarshalJSON("*** Begin Patch\n"+body+"\n*** End Patch\n"))
			}
			history := mekugiHistory{ToolName: "exec", NativePatches: nativePatchesInCall("exec", source.String(), workspace)}
			if len(history.NativePatches) != 2 {
				t.Fatalf("missing patch observations: %+v", history.NativePatches)
			}
			history.nativeCell = &nativeTraceCell{ended: true}
			for index, observation := range history.NativePatches {
				history.nativeCell.tools = append(history.nativeCell.tools, &nativeTraceTool{
					CallID: fmt.Sprintf("patch-%d", index), Tool: applyPatchToolName, Status: "completed",
					input: observation.Input, terminal: true,
				})
			}
			if err := proxy.finalizeNativePatches(t.Context(), workspace, "thread", "cell", history, mustMarshalJSON("Script running with cell ID 7\nWall time 0.1 seconds\nOutput:\n")); err != nil {
				t.Fatal(err)
			}
			if _, found, err := proxy.replayStore.lookup(t.Context(), workspace, nativePatchDerivedCallID("cell", 0)); err != nil || found {
				t.Fatalf("yielded cell finalized: %v, %v", found, err)
			}
			// Model the actual terminal workspace, without evaluating patch inputs.
			if test.finalPath != "file.txt" {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(workspace, test.finalPath), []byte("final\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for range 2 { // Reconciliation replay must not allocate duplicate evidence.
				if err := proxy.finalizeNativePatches(t.Context(), workspace, "thread", "cell", history, mustMarshalJSON("Script completed\nWall time 0.1 seconds\nOutput:\n")); err != nil {
					t.Fatal(err)
				}
			}
			var ids []string
			seen := make(map[string]bool)
			for index := range history.NativePatches {
				attempt, found, err := proxy.replayStore.lookup(t.Context(), workspace, nativePatchDerivedCallID("cell", index))
				if err != nil || !found || attempt.Script != history.NativePatches[index].Input || attempt.AlreadySatisfied {
					t.Fatalf("lost or misreported attempt: %+v, %v", attempt, err)
				}
				if len(attempt.HostResults) != 1 || attempt.HostResults[0].Status != "completed" {
					t.Fatalf("missing confirmed nested success: %+v", attempt.HostResults)
				}
				if attempt.ChangeID != "" {
					ids = append(ids, attempt.ChangeID)
				}
				for _, file := range attempt.ReviewFiles {
					for i, path := range []string{file.BeforePath, file.AfterPath} {
						if path == "" || i == 1 && path == file.BeforePath {
							continue
						}
						if seen[path] {
							t.Fatalf("duplicate evidence for %s", path)
						}
						seen[path] = true
					}
				}
			}
			net, err := proxy.replayStore.readChanges(t.Context(), changeReadOptions{workspace: workspace, ids: ids, view: "net", maxTokens: 4000})
			if err != nil || strings.Count(net, "-old\n") != 1 || strings.Count(net, "+final\n") != 1 || strings.Contains(net, "middle") {
				t.Fatalf("cell-window net = %q, %v", net, err)
			}
		})
	}
}

func TestNativePatchReviewChecksActualDeletionAndMove(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "source.txt")
	if err := os.WriteFile(source, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, patch := range []string{
		"*** Begin Patch\n*** Delete File: source.txt\n*** End Patch\n",
		"*** Begin Patch\n*** Update File: source.txt\n*** Move to: destination.txt\n@@\n-before\n+after\n*** End Patch\n",
	} {
		observation, err := captureNativePatch(patch, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if reviews, complete := nativePatchReview(observation.Files); !complete || len(reviews) != 0 {
			t.Fatalf("unchanged deletion or move fabricated a diff: %+v, complete=%t", reviews, complete)
		}
		if strings.Contains(patch, "Move to") {
			destination := filepath.Join(workspace, "destination.txt")
			if err := os.WriteFile(destination, []byte("copied\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			reviews, complete := nativePatchReview(observation.Files)
			if !complete || len(reviews) != 1 || reviews[0].Action() != mekugi.ReviewAdd {
				t.Fatalf("partial move was not reported as destination creation: %+v, complete=%t", reviews, complete)
			}
			if err := os.Remove(destination); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestNativePatchReviewShowsOnlyObservedPartialCodeModeEffect(t *testing.T) {
	workspace := t.TempDir()
	first := filepath.Join(workspace, "first.txt")
	second := filepath.Join(workspace, "second.txt")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	patch := "*** Begin Patch\n*** Update File: first.txt\n@@\n-old\n+new\n*** Update File: second.txt\n@@\n-old\n+new\n*** End Patch\n"
	observation, err := captureNativePatch(patch, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reviews, complete := nativePatchReview(observation.Files)
	if !complete || len(reviews) != 1 || reviews[0].AfterPath != first {
		t.Fatalf("partial effect = %+v, complete=%t", reviews, complete)
	}
}

func TestCodeModeMoveBaselinesShareRequestCaptureBudget(t *testing.T) {
	workspace := t.TempDir()
	var script strings.Builder
	for index := range 4 {
		source := fmt.Sprintf("source-%d.txt", index)
		target := fmt.Sprintf("target-%d.txt", index)
		if err := os.WriteFile(filepath.Join(workspace, source), []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, target), []byte(strings.Repeat("x", 7<<20)), 0o600); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: " + source + "\n*** Move to: " + target + "\n@@\n-old\n+new\n*** End Patch"
		fmt.Fprintf(&script, "await tools.apply_patch(%q);\n", patch)
	}
	if patches := nativePatchesInCall("exec", script.String(), workspace); len(patches) != 0 {
		t.Fatalf("oversized combined move baselines were retained: %d", len(patches))
	}
}
