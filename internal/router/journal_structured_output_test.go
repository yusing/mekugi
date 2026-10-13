package router

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestJournalStructuredOutputPreservesProviderFinal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, child := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/child=%v", stream, child), func(t *testing.T) {
				proxy, workspace := newManagedMekugiProxy(t), t.TempDir()
				format := map[string]any{"format": map[string]any{"type": "json_schema", "name": "review", "strict": true, "schema": jsontext.Value(orchestrate.ReviewResultSchema)}}
				request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
					"model": "gpt-test", "input": []any{map[string]any{"role": "user", "content": "Review"}},
					"tools": testExecResponsesTools(), "text": format,
				}))
				if err != nil {
					t.Fatal(err)
				}
				transform, err := proxy.prepareRequest(t.Context(), &request, "session", "reviewer", codexTurnMetadata{RequestKind: "turn", TurnID: "review", Directories: map[string]jsonv1.RawMessage{workspace: nil}}, true)
				if err != nil {
					t.Fatal(err)
				}
				defer transform.Close()
				transform.subagentTurn = child
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "reviewer", "progress", []journalMutation{{Op: "add", Text: new("Source review complete")}}); err != nil {
					t.Fatal(err)
				}
				if !stream && !child {
					host := map[string]jsonv1.RawMessage{"type": mustMarshalJSON("function_call"), "call_id": mustMarshalJSON("host"), "name": mustMarshalJSON("exec_command")}
					transform.visible["host"] = mekugiHistory{ToolName: "exec_command", ExecutingThread: "reviewer", JournalFinishTurnID: "review", CarrierKind: codeModeCarrierFunction, CarrierName: "exec_command", UpstreamItem: host}
					if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "reviewer", "runtime:"+journalHostFinishReceipt("review", "host"), nil); err != nil {
						t.Fatal(err)
					}
					request.fields["input"] = mustMarshalJSON([]any{host, map[string]any{"type": "function_call_output", "call_id": "host", "output": "Wall time: 0.1 seconds\nProcess exited with code 0\nOutput:\n"}})
					if finished, err := transform.journalHostFinished(request.fields["input"]); err != nil || !finished {
						t.Fatalf("host completion was not established: %v", err)
					}
					attempt := &requestAttempt{startCtx: t.Context(), mekugiTransform: transform, request: request}
					if finished, err := attempt.tryJournalHostFinish(); err != nil || finished {
						t.Fatalf("journal bypassed schema-constrained inference: %v", err)
					}
				}
				verdict := `{"revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","purpose":"correctness","verdict":"blocked","findings":[],"reason":"Required runtime evidence is unavailable"}`
				answer := map[string]any{"type": "message", "id": "verdict", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": verdict}}}
				response := map[string]any{"id": "review-response", "status": "completed", "output": []any{answer}}
				var wire []byte
				if stream {
					var events [][]byte
					for _, payload := range []any{map[string]any{"type": "response.output_item.done", "item": answer}, map[string]any{"type": "response.completed", "response": response}} {
						out, err := transform.TransformSSE(mustTestJSON(t, payload))
						if err != nil {
							t.Fatal(err)
						}
						events = append(events, out...)
						transform.ReleaseDelivery()
					}
					wire = []byte(finalAnswerTestWire(events))
				} else {
					wire, err = transform.TransformJSON(mustTestJSON(t, response))
					if err != nil {
						t.Fatal(err)
					}
				}
				finals := 0
				for _, item := range journalFinishClientOutput(t, stream, wire) {
					if jsonString(item, "phase") == "final_answer" {
						finals++
						if jsonString(item, "id") != "verdict" || commentaryMessageText(item) != verdict {
							t.Fatalf("provider verdict changed: %s", wire)
						}
					}
				}
				if finals != 1 {
					t.Fatalf("got %d final answers: %s", finals, wire)
				}
			})
		}
	}
}
