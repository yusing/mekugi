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
	t.Parallel()
	for _, protocol := range []string{"native", "ctp2"} {
		for _, snapshot := range []string{"empty", "absent", "complete", "snapshot-only", "missing-workspace"} {
			t.Run(protocol+"/"+snapshot, func(t *testing.T) {
				omitWorkspace := snapshot == "missing-workspace"
				if omitWorkspace {
					snapshot = "complete"
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				proxy := newManagedMekugiProxy(t)
				var storeErr error
				proxy.replayStore, storeErr = openMekugiReplayStore(t.TempDir())
				if storeErr != nil {
					t.Fatal(storeErr)
				}
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
						if i == 1 {
							wantParent := "finished"
							if omitWorkspace {
								wantParent = ""
							}
							if jsonString(request, "previous_response_id") != wantParent {
								t.Errorf("next turn parent = %q, want %q", jsonString(request, "previous_response_id"), wantParent)
							}
							var input []map[string]json.RawMessage
							if err := json.Unmarshal(request["input"], &input); err != nil {
								t.Error(err)
								return
							}
							if !omitWorkspace && (len(input) != 2 || jsonString(input[0], "call_id") != "finished-call" || jsonString(input[1], "role") != "user") {
								t.Errorf("expected only missing journal result followed by user input: %s", request["input"])
							}
							resultSeen := false
							for _, item := range input {
								if omitWorkspace {
									if journalResultCallID(item) == "finished-call" && jsonString(item, "name") == journalToolName {
										resultSeen = true
									}
									if isJournalCall(item) && jsonString(item, "call_id") == "finished-call" {
										t.Error("replay borrowed a call from an unselected workspace")
									}
								} else if jsonString(item, "type") == "function_call_output" && jsonString(item, "call_id") == "finished-call" {
									resultSeen = true
								}
							}
							if !resultSeen {
								t.Errorf("next turn omitted finish result: %s", request["input"])
							}
						}
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
				next := map[string]any{
					"type": "response.create", "previous_response_id": "finished",
					"input": []any{map[string]any{"type": "message", "role": "user", "content": "List my journal."}},
				}
				if omitWorkspace {
					// Codex enriches each new turn's workspace metadata asynchronously.
					// The current envelope can override a populated handshake with no workspace.
					next["client_metadata"] = map[string]string{codexTurnMetadataHeader: `{"request_kind":"turn"}`}
				}
				socketWrite(t, ctx, conn, next)
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
