package router

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestStorageCompletionIndependentOfAnswerPresentation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"empty", "child", "refusal", "disabled", "tool", "streamed-tool", "undelivered", "failed", "incomplete", "write-failure"} {
			if (scenario == "streamed-tool" || scenario == "write-failure") && !stream {
				continue
			}
			t.Run(fmt.Sprintf("stream=%t/%s", stream, scenario), func(t *testing.T) {
				t.Parallel()
				store, err := openMekugiReplayStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				proxy := newManagedMekugiProxy(t)
				setTestProxyStore(proxy, store)
				request := activityAdmissionRequest(t, nil)
				transform, err := proxy.prepareRequest(t.Context(), &request, "routing", "thread", codexresponses.TurnMetadata{RequestKind: "turn", ThreadID: "thread", Directories: map[string]json.RawMessage{t.TempDir(): nil}}, true)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(transform.Close)
				transform.subagentTurn = scenario == "child"
				transform.finalAnswer.disabled = scenario == "disabled"
				lease := flock.New(filepath.Join(store.directory, fmt.Sprintf("session-%x.lock", sha256.Sum256([]byte("thread")))))
				assertLeased := func(want bool) {
					t.Helper()
					acquired, err := lease.TryLock()
					if acquired {
						_ = lease.Unlock()
					}
					if err != nil || acquired == want {
						t.Fatalf("lease held=%v want=%v err=%v", !acquired, want, err)
					}
				}
				status := "completed"
				if scenario == "failed" || scenario == "incomplete" {
					status = scenario
				}
				output := []any{}
				tool := map[string]any{"type": "function_call", "id": "tool", "call_id": "call", "name": "unrelated", "arguments": "{}", "status": "completed"}
				if scenario == "tool" {
					output = append(output, tool)
				}
				if scenario == "refusal" {
					output = append(output, map[string]any{"type": "message", "id": "answer", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "refusal", "refusal": "Unavailable."}}})
				}
				if scenario == "streamed-tool" {
					_, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": tool}))
					if err != nil {
						t.Fatal(err)
					}
				}
				response := map[string]any{"id": "response", "status": status, "output": output}
				var delivered [][]byte
				if stream {
					wire := mustTestJSON(t, map[string]any{"type": "response." + status, "response": response})
					if scenario == "write-failure" {
						_, err := copySSETransformed(serverErrorWriter{err: errors.New("disconnected")}, strings.NewReader("data: "+string(wire)+"\n\n"), transform, nil)
						if err == nil {
							t.Fatal("failed writer accepted terminal")
						}
					} else {
						delivered, err = transform.TransformSSE(wire)
					}
				} else {
					var wire []byte
					wire, err = transform.TransformJSON(mustTestJSON(t, response))
					delivered = [][]byte{wire}
				}
				if err != nil {
					t.Fatal(err)
				}
				assertLeased(true)
				if scenario != "undelivered" {
					for _, payload := range delivered {
						transform.Delivered(payload)
					}
				}
				assertLeased(true)
				transform.Close()
				keep := scenario == "tool" || scenario == "streamed-tool" || scenario == "undelivered" || scenario == "failed" || scenario == "incomplete" || scenario == "write-failure"
				assertLeased(keep)
			})
		}
	}
}
