package router

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompactionCapturesCompletedNativePatch(t *testing.T) {
	testCompactionCapture(t, "patch", false)
}

func TestCompactionCapturesFailedNativePatchWithoutSuccess(t *testing.T) {
	testCompactionCapture(t, "patch", true)
}

func TestCompactionCapturesCompletedNativeExecWrite(t *testing.T) {
	testCompactionCapture(t, "exec", false)
}

func TestCompactionCapturesCompletedCodeModePatch(t *testing.T) {
	testCompactionCapture(t, "exec", false)
}

// The host result first becomes visible in compaction, not in an ordinary
// follow-up turn. A fresh proxy must finish the durable pre-call observation
// without requiring a live transform or changing the compaction payload.
func testCompactionCapture(t *testing.T, tool string, failed bool) {
	t.Helper()
	for _, mode := range []string{"auto", "off"} {
		for _, missingWorkspace := range []bool{false, true} {
			name := mode + "/workspace-present"
			if missingWorkspace {
				name = mode + "/workspace-recovered"
			}
			t.Run(name, func(t *testing.T) {
				workspace := t.TempDir()
				firstProxy := newManagedMekugiProxy(t)
				attachTestReplayStore(t, firstProxy)
				storeDirectory := firstProxy.replayStore.directory
				var transform *mekugiResponseTransform
				var trace *nativeTraceFixture
				if tool == "exec" {
					trace = newNativeTraceFixture(t)
					firstProxy.nativeTrace = &nativeToolTrace{directory: trace.root}
					transform, _, _, workspace = newMekugiTestTransformWithProxy(t, firstProxy)
				} else {
					transform = prepareNativeStockTransform(t, firstProxy, workspace, "before-compaction")
				}
				target := filepath.Join(workspace, "file.txt")
				writeTestFile(t, target, "old\n")
				thread := transform.shellThreadID
				var items []any
				var derivedCallID string
				if tool == "patch" {
					patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch\n"
					retainPatchObservation(t, transform, patch)
					result := "Success. Updated the following files:\nM file.txt\n"
					if failed {
						result = "Error: patch failed"
					}
					items = []any{
						map[string]any{"type": "custom_tool_call", "id": "patch-item", "call_id": "patch-call", "name": applyPatchToolName, "input": patch, "status": "completed"},
						map[string]any{"type": "custom_tool_call_output", "call_id": "patch-call", "output": result},
					}
					derivedCallID = nativePatchDerivedCallID("patch-call", 0)
				} else if tool == "exec" {
					patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch\n"
					source := "await tools.apply_patch(" + string(mustTestJSON(t, patch)) + ");"
					call := map[string]any{"type": "custom_tool_call", "id": "code-item", "call_id": "code-call", "name": "exec", "input": source, "status": "completed"}
					if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "response", "status": "completed", "output": []any{call}})); err != nil {
						t.Fatal(err)
					}
					trace.start(thread, "runtime-cell", "code-call", source)
					trace.tool(thread, "runtime-cell", "nested-patch", "apply_patch", patch)
					trace.result(thread, "nested-patch", "completed", map[string]any{})
					trace.end(thread, "runtime-cell")
					items = []any{call, map[string]any{"type": "custom_tool_call_output", "call_id": "code-call", "output": []any{
						map[string]any{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"},
					}}}
					derivedCallID = nativePatchDerivedCallID("code-call", 0)
				} else {
					arguments := string(mustTestJSON(t, map[string]any{"cmd": "printf 'new\\n' > file.txt", "workdir": workspace}))
					retainCommandObservation(t, transform, "exec-call", arguments)
					items = []any{
						map[string]any{"type": "function_call", "call_id": "exec-call", "name": nativeExecCommandToolName, "arguments": arguments, "status": "completed"},
						map[string]any{"type": "function_call_output", "call_id": "exec-call", "output": nativeExecOutput("Process exited with code 0")},
					}
					derivedCallID = execDerivedCallID("exec-call", false)
				}
				if _, found, err := firstProxy.replayStore.lookup(t.Context(), workspace, derivedCallID); err != nil || found {
					t.Fatalf("terminal evidence existed before the host result: found=%t err=%v", found, err)
				}
				transform.Close()
				if err := firstProxy.Close(); err != nil {
					t.Fatal(err)
				}
				// Simulate the authorized stock host effect, not router execution.
				if !failed {
					writeTestFile(t, target, "new\n")
				}
				proxy := newManagedMekugiProxy(t)
				if trace != nil {
					proxy.nativeTrace = &nativeToolTrace{directory: trace.root}
				}
				var err error
				proxy.replayStore, err = openMekugiReplayStore(storeDirectory)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(proxy.replayStore.snapshots.close)
				proxy.journalCompaction = mode
				request, headers := journalCompactionRequest(t, workspace, thread)
				request.fields["input"] = mustTestJSON(t, append(items, map[string]any{"role": "user", "content": "Summarize."}))
				if missingWorkspace {
					metadata, _ := decodeCodexTurnMetadata(headers)
					metadata.Directories = nil
					headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
				}
				original, err := request.wireBody(request.fields)
				if err != nil {
					t.Fatal(err)
				}
				wire, err := journalCompactionSSE("upstream", "gpt-test", "Provider summary")
				if err != nil {
					t.Fatal(err)
				}
				var changeID string
				for observation := range 2 {
					response := serverHTTPResponse(string(wire))
					response.Header.Set("Content-Type", "text/event-stream")
					provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
					var output bytes.Buffer
					if err := executeRequest(t.Context(), t.Context(), request, headers, "compact", provider, &output, nil, proxy); err != nil {
						t.Fatal(err)
					}
					if mode == "off" {
						if len(provider.forwarded) != 1 || !bytes.Equal(provider.forwarded[0], original) || !bytes.Equal(output.Bytes(), wire) {
							t.Fatalf("compaction observation altered provider request/response: forwarded=%q output=%s", provider.forwarded, &output)
						}
					} else if len(provider.forwarded) != 0 || !strings.Contains(output.String(), "response.completed") {
						t.Fatalf("local compaction was not delivered: forwards=%d output=%s", len(provider.forwarded), &output)
					}
					unchanged, err := request.wireBody(request.fields)
					if err != nil || !bytes.Equal(unchanged, original) {
						t.Fatalf("observation mutated caller-owned request: %s, %v", unchanged, err)
					}
					// Read through a reopened store so in-memory state cannot stand in
					// for evidence persisted before the compaction response.
					reopened, err := openMekugiReplayStore(storeDirectory)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(reopened.snapshots.close)
					readCtx, release, err := reopened.beginSession(t.Context(), thread, "compact-review")
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(release)
					proxy.replayStore = reopened
					reopened = reopened.scoped(readCtx)
					history, found, err := reopened.lookup(readCtx, workspace, derivedCallID)
					if err != nil || !found {
						t.Fatalf("compaction lost completed observation: found=%t err=%v", found, err)
					}
					if failed {
						if history.TranslationError == "" || history.ChangeID != "" || len(history.ReviewFiles) != 0 || history.AlreadySatisfied || len(history.CommentaryMessageIDs) != 0 {
							t.Fatalf("unchanged failed patch claimed success: %+v", history)
						}
					} else {
						if tool == "exec" && (len(history.HostResults) != 1 || history.HostResults[0].Status != "completed") {
							t.Fatalf("nested patch host result was not recovered: %+v", history.HostResults)
						}
						if history.ChangeID == "" || len(history.ReviewFiles) != 1 || history.TranslationError != "" {
							t.Fatalf("successful effect missing durable evidence: %+v", history)
						}
						if observation > 0 && history.ChangeID != changeID {
							t.Fatalf("repeated compaction allocated a new change: %q -> %q", changeID, history.ChangeID)
						}
						changeID = history.ChangeID
						changes, err := reopened.readChanges(readCtx, changeReadOptions{workspace: workspace, ids: []string{changeID}, maxTokens: 4000})
						if err != nil || strings.Count(changes, "-old\n") != 1 || strings.Count(changes, "+new\n") != 1 {
							t.Fatalf("retained changes lost or duplicated actual diff: %q, %v", changes, err)
						}
						if mode == "auto" && (!strings.Contains(output.String(), "Retained changes:") || !strings.Contains(output.String(), changeID) || !strings.Contains(output.String(), "file.txt")) {
							t.Fatalf("local compaction summary omitted just-completed change: %s", &output)
						}
					}
					summary, err := summaryForTest(t, readCtx, reopened, workspace, thread)
					if err != nil {
						t.Fatal(err)
					}
					if !failed && (!strings.Contains(summary.Text, "Retained changes:") || !strings.Contains(summary.Text, changeID) || !strings.Contains(summary.Text, "file.txt")) {
						t.Fatalf("durable journal summary omitted change: %s", summary.Text)
					}
					if failed && summary.Changes != 0 {
						t.Fatalf("failed unchanged patch counted as a change: %+v", summary)
					}
					release()
				}
				content, err := os.ReadFile(target)
				want := "new\n"
				if failed {
					want = "old\n"
				}
				if err != nil || string(content) != want {
					t.Fatalf("compaction changed host workspace: %q, %v", content, err)
				}
			})
		}
	}
}
