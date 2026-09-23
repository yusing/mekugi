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
		"tools": testNativeResponsesTools(), "tool_choice": "auto",
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

func streamNativePatch(t *testing.T, transform *mekugiResponseTransform, patch string, broker *liveDiffBroker, subscriber *liveDiffSubscriber) {
	t.Helper()
	added := mustMarshalJSON(map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{
			"type": "custom_tool_call", "id": "patch-item", "call_id": "patch-call",
			"name": applyPatchToolName, "input": "", "status": "in_progress",
		},
	})
	delta := mustMarshalJSON(map[string]any{
		"type": "response.custom_tool_call_input.delta", "item_id": "patch-item", "delta": patch,
	})
	done := mustMarshalJSON(map[string]any{
		"type": "response.custom_tool_call_input.done", "item_id": "patch-item",
		"call_id": "patch-call", "input": patch,
	})
	for _, event := range [][]byte{added, delta} {
		visible, err := transform.TransformSSE(event)
		if err != nil || len(visible) != 1 || string(visible[0]) != string(event) {
			t.Fatalf("stock stream changed: visible=%q err=%v", visible, err)
		}
	}
	if subscriber != nil {
		preview := waitLiveDiffWorkerPreview(t, broker, subscriber, func(preview liveDiffPreview) bool {
			return preview.DiffText && strings.Contains(preview.Input, "*** Begin Patch")
		})
		if !strings.Contains(preview.Input, patch) {
			t.Fatalf("preview = %+v", preview)
		}
	}
	visible, err := transform.TransformSSE(done)
	if err != nil || len(visible) != 1 || string(visible[0]) != string(done) {
		t.Fatalf("stock completion changed: visible=%q err=%v", visible, err)
	}
	history, found := transform.local["patch-call"]
	if !found || len(history.NativePatches) != 1 {
		t.Fatalf("stock apply_patch was not retained before exposure: found=%v history=%+v", found, history)
	}
}

func reconcileNativePatchResult(t *testing.T, proxy *mekugiProxy, workspace, patch, output string) mekugiHistory {
	t.Helper()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "tools": testNativeResponsesTools(), "tool_choice": "auto",
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

func TestNativeApplyPatchStreamsBeforeCompletionAndPersistsMChangesEvidence(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch\n"
	transform := prepareNativeStockTransform(t, proxy, workspace, "stock-session")
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"stock-thread": true}}})
	subscriber := broker.subscribe()
	<-subscriber.events
	proxy.autoLiveDiff = &autoLiveDiff{
		events: broker, requested: true,
		scope: liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"stock-thread": true}}},
	}
	proxy.autoLiveDiff.enabled.Store(true)
	streamNativePatch(t, transform, patch, broker, subscriber)

	retained, found, err := proxy.replayStore.lookup(transform.ctx, workspace, "patch-call")
	if err != nil || !found || retained.Script != patch || len(retained.NativePatches) != 1 {
		t.Fatalf("pre-execution evidence = %+v, found=%v err=%v", retained, found, err)
	}
	if err := os.WriteFile(target, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	history := reconcileNativePatchResult(t, proxy, workspace, patch, "Success. Updated the following files:\nM file.txt\n")
	if !history.Applied || history.ChangeID == "" || len(history.ReviewFiles) != 1 {
		t.Fatalf("completed evidence = %+v", history)
	}
	changes, err := proxy.replayStore.readChanges(transform.ctx, changeReadOptions{
		workspace: workspace, ids: []string{history.ChangeID}, maxTokens: 4000,
	})
	if err != nil || !strings.Contains(changes, "-old") || !strings.Contains(changes, "+new") {
		t.Fatalf("mchanges evidence = %q, %v", changes, err)
	}
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
			streamNativePatch(t, transform, patch, nil, nil)
			if err := os.WriteFile(target, []byte(test.after), 0o600); err != nil {
				t.Fatal(err)
			}
			history := reconcileNativePatchResult(t, proxy, workspace, patch, "Error: patch failed")
			if history.Applied || history.TranslationError == "" || (len(history.ReviewFiles) != 0) != test.wantReview {
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
		t.Fatalf("ordinary Code Mode patch observation = %+v", observed)
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
			reconcile(items)
			history, found, err := proxy.replayStore.lookup(transform.ctx, workspace, nativePatchDerivedCallID("code-call", 0))
			if err != nil || !found {
				t.Fatalf("completed patch missing: found=%v err=%v", found, err)
			}
			if history.Applied || history.AlreadySatisfied || history.TranslationError != "" ||
				(len(history.ReviewFiles) != 0) != test.change {
				t.Fatalf("completed patch = %+v", history)
			}
			listed, err := proxy.replayStore.readChanges(transform.ctx, changeReadOptions{workspace: workspace, view: "list"})
			if err != nil || listed != history.ChangeID+"\n" {
				t.Fatalf("mchanges list = %q, %v; want %q", listed, err, history.ChangeID)
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
		if reviews, complete := nativePatchReview(observation.Files, nil); !complete || len(reviews) != 0 {
			t.Fatalf("unchanged deletion or move fabricated a diff: %+v, complete=%t", reviews, complete)
		}
		if strings.Contains(patch, "Move to") {
			destination := filepath.Join(workspace, "destination.txt")
			if err := os.WriteFile(destination, []byte("copied\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			reviews, complete := nativePatchReview(observation.Files, nil)
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
	reviews, complete := nativePatchReview(observation.Files, nil)
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
