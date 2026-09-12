package router

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestJournalFinishWebSocketDoesNotContinueOrFinishLaterTurn(t *testing.T) {
	for _, protocol := range []string{"native", "ctp2"} {
		for _, snapshot := range []string{"empty", "absent", "complete", "snapshot-only"} {
			t.Run(protocol+"/"+snapshot, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
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
				headers.Set(sessionIDHeader, "finish-session")
				requests := make(chan map[string]json.RawMessage, 3)
				conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstream, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer upstream.CloseNow()
					upstream.SetReadLimit(upstreamJSONBufferBytes)
					for i, id := range []string{"finished", "listing", "later-terminal"} {
						request, err := providerSocketRead(ctx, upstream)
						if err != nil {
							t.Error(err)
							return
						}
						requests <- request
						if err := providerSocketWrite(ctx, upstream, socketEvent("response.created", id)); err != nil {
							t.Error(err)
							return
						}
						var item map[string]any
						if i < 2 {
							args := `{"op":"finish","journal":[{"op":"add","text":"Done"}]}`
							if i == 1 {
								args = `{"op":"list"}`
							}
							item = map[string]any{"type": "function_call", "id": id + "-item", "call_id": id + "-call", "name": "journal", "arguments": args, "status": "completed"}
							if snapshot != "snapshot-only" {
								if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}); err != nil {
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
							response["output"] = []any{item}
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
				readTerminal := func(want string) []map[string]json.RawMessage {
					var delivered []map[string]json.RawMessage
					t.Helper()
					for {
						event := socketRead(t, ctx, conn)
						delivered = append(delivered, event)
						if jsonString(event, "type") == "error" {
							t.Fatalf("router error: %s", mustMarshalJSON(event))
						}
						if jsonString(event, "type") == "response.completed" {
							var response map[string]json.RawMessage
							if err := json.Unmarshal(event["response"], &response); err != nil {
								t.Fatal(err)
							}
							if got := jsonString(response, "id"); got != want {
								t.Fatalf("terminal = %q, want %q", got, want)
							}
							return delivered
						}
					}
				}
				readTerminal("finished")
				if len(requests) != 1 {
					t.Fatalf("finish made %d requests, want 1", len(requests))
				}
				socketWrite(t, ctx, conn, map[string]any{
					"type": "response.create", "previous_response_id": "finished",
					"input": []any{map[string]any{"type": "message", "role": "user", "content": "List my journal."}},
				})
				for _, event := range readTerminal("later-terminal") {
					if wire := string(mustMarshalJSON(event)); strings.Contains(wire, "Journal flush") && strings.Contains(wire, "Done") {
						t.Fatal("later turn repeated the completed journal flush")
					}
				}
				if len(requests) != 3 {
					t.Fatalf("later list did not continue normally: %d requests", len(requests))
				}
			})
		}
	}
}
