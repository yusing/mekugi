package router

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestJournalWebSocketContinuationRetainsStreamedCall(t *testing.T) {
	for _, protocol := range []string{"native", "ctp2"} {
		for _, snapshot := range []string{"empty", "absent", "complete", "snapshot-only"} {
			t.Run(protocol+"/"+snapshot, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				translations := 0
				proxy := newManagedMekugiProxy(t, testTranslator(t, &translations))
				proxy.customizedInstructions = true
				proxy.compactModelProtocol = protocol == "ctp2"
				var codec *ctp2Codec
				if protocol == "ctp2" {
					var err error
					codec, err = newCTP2Codec()
					if err != nil {
						t.Fatal(err)
					}
				}
				headers := codexAuthHeaders()
				metadata := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
				headers.Set(codexTurnMetadataHeader, metadata.Get(codexTurnMetadataHeader))
				headers.Set(threadIDHeader, "thread-1")
				headers.Set(sessionIDHeader, "journal-session")
				requests := make(chan map[string]json.RawMessage, 1)
				conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstream, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer upstream.CloseNow()
					upstream.SetReadLimit(upstreamJSONBufferBytes)
					for i, id := range []string{"journal-response", "tool-response", "final-response"} {
						request, err := providerSocketRead(ctx, upstream)
						if err != nil {
							t.Error(err)
							return
						}
						if i == 2 {
							requests <- request
						}
						if err := providerSocketWrite(ctx, upstream, socketEvent("response.created", id)); err != nil {
							t.Error(err)
							return
						}
						var item map[string]any
						if i == 0 {
							item = map[string]any{"type": "function_call", "id": "journal-item", "call_id": "journal-call", "name": "journal", "arguments": `{"op":"list"}`, "status": "completed"}
						} else if i == 1 {
							item = testMekugiItem()
							item["status"] = "completed"
						}
						if item != nil {
							if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}); err != nil {
								t.Error(err)
								return
							}
						}
						var message map[string]any
						if i == 0 {
							message = map[string]any{
								"type": "message", "id": "provider-message", "role": "assistant", "phase": "final_answer", "status": "completed",
								"content": []any{map[string]any{"type": "output_text", "text": "Preserve this unexpected provider message."}},
							}
							if snapshot != "snapshot-only" {
								if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.output_item.done", "output_index": 1, "item": message}); err != nil {
									t.Error(err)
									return
								}
							}
						}
						terminal := socketEvent("response.completed", id)
						response := terminal["response"].(map[string]any)
						if snapshot == "absent" {
							delete(response, "output")
						} else if (snapshot == "complete" || snapshot == "snapshot-only") && item != nil {
							items := []any{item}
							if message != nil {
								items = append(items, message)
							}
							response["output"] = items
						}
						if err := providerSocketWrite(ctx, upstream, terminal); err != nil {
							t.Error(err)
							return
						}
					}
				}), proxy, codec, headers)
				request := serverRequest(t, nil).fields
				request["type"] = mustMarshalJSON("response.create")
				request["instructions"] = mustMarshalJSON("Follow the task.")
				socketWrite(t, ctx, conn, request)
				for {
					event := socketRead(t, ctx, conn)
					if jsonString(event, "type") == "error" {
						t.Fatalf("router error: %s", mustMarshalJSON(event))
					}
					if jsonString(event, "type") == "response.completed" {
						var response map[string]json.RawMessage
						if err := json.Unmarshal(event["response"], &response); err != nil {
							t.Fatal(err)
						}
						if got := jsonString(response, "id"); got != "tool-response" {
							t.Fatalf("first terminal = %q, want tool-response", got)
						}
						break
					}
				}
				socketWrite(t, ctx, conn, map[string]any{
					"type": "response.create", "previous_response_id": "tool-response",
					"input": []any{map[string]any{"type": "custom_tool_call_output", "call_id": "call-H", "output": "done"}},
				})
				select {
				case next := <-requests:
					if jsonString(next, "previous_response_id") != "" {
						t.Fatal("journal replay must rebase the named result")
					}
					var input []map[string]json.RawMessage
					if err := json.Unmarshal(next["input"], &input); err != nil {
						t.Fatal(err)
					}
					calls, results, messages := 0, 0, 0
					for _, item := range input {
						if jsonString(item, "id") == "provider-message" {
							messages++
							if !bytes.Contains(item["content"], []byte("Preserve this unexpected provider message.")) {
								t.Fatalf("provider message changed in replay: %s", mustMarshalJSON(item))
							}
						}
						if jsonString(item, "call_id") != "call-H" {
							continue
						}
						switch jsonString(item, "type") {
						case "custom_tool_call":
							calls++
							if jsonString(item, "name") != mekugiToolName || jsonString(item, "input") != testMekugiScript {
								t.Error("replay did not restore the original model call")
							}
						case "custom_tool_call_output":
							results++
						}
					}
					if messages != 1 {
						t.Fatalf("replay retained %d provider messages, want 1: %s", messages, next["input"])
					}
					if calls != 1 || results != 1 {
						t.Fatalf("replayed call/result counts = %d/%d, want 1/1", calls, results)
					}
				case <-ctx.Done():
					t.Fatal("tool result never reached provider")
				}
				for {
					event := socketRead(t, ctx, conn)
					if jsonString(event, "type") == "error" {
						t.Fatalf("router error: %s", mustMarshalJSON(event))
					}
					if jsonString(event, "type") == "response.completed" {
						break
					}
				}
				if translations != 1 {
					t.Fatalf("tool translated %d times, want 1", translations)
				}
			})
		}
	}
}
