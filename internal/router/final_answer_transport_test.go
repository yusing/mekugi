package router

import (
	"bufio"
	"bytes"
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The provider cannot send the next event until the downstream consumer receives
// this one. A terminal-only dump therefore times out rather than passing on the
// final accumulated output.
func TestFinalAnswerTransportStreamsBeforeTerminal(t *testing.T) {
	for _, upstreamWS := range []bool{false, true} {
		for _, downstreamWS := range []bool{false, true} {
			// Native downstream WebSocket sessions require an upstream WebSocket.
			if downstreamWS && !upstreamWS {
				continue
			}
			for _, child := range []bool{false, true} {
				for _, status := range []string{"completed", "failed", "incomplete"} {
					name := map[bool]string{false: "http", true: "ws"}[upstreamWS] + "/" + map[bool]string{false: "http", true: "ws"}[downstreamWS] + "/" + map[bool]string{false: "main", true: "child"}[child] + "/" + status
					t.Run(name, func(t *testing.T) {
						ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
						defer cancel()
						proxy := newManagedMekugiProxy(t)
						events := finalAnswerTestEvents(t, "final_answer")
						// Exercise more than one increment of answer text.
						events = append(events[:2:2], append([][]byte{
							mustTestJSON(t, map[string]any{"type": "response.output_text.delta", "item_id": "answer", "output_index": 0, "content_index": 0, "delta": "No files "}),
							mustTestJSON(t, map[string]any{"type": "response.output_text.delta", "item_id": "answer", "output_index": 0, "content_index": 0, "delta": "were changed."}),
						}, events[3:]...)...)
						terminal := finalAnswerTestTerminal(t, status, true)
						received := make(chan struct{})
						providerDone := make(chan error, 1)
						upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							var send func([]byte) error
							if upstreamWS {
								conn, err := websocket.Accept(w, r, nil)
								if err != nil {
									providerDone <- err
									return
								}
								defer conn.CloseNow()
								if _, err := providerSocketRead(ctx, conn); err != nil {
									providerDone <- err
									return
								}
								send = func(payload []byte) error { return conn.Write(ctx, websocket.MessageText, payload) }
							} else {
								if _, err := io.Copy(io.Discard, r.Body); err != nil {
									providerDone <- err
									return
								}
								w.Header().Set("Content-Type", "text/event-stream")
								send = func(payload []byte) error {
									if _, err := io.WriteString(w, finalAnswerTestWire([][]byte{payload})); err != nil {
										return err
									}
									return http.NewResponseController(w).Flush()
								}
							}
							if err := send(mustMarshalJSON(socketEvent("response.created", "response"))); err != nil {
								providerDone <- err
								return
							}
							for _, event := range events {
								if err := send(event); err != nil {
									providerDone <- err
									return
								}
								select {
								case <-received:
								case <-ctx.Done():
									providerDone <- ctx.Err()
									return
								}
							}
							providerDone <- send(terminal)
						}))
						defer upstream.Close()
						client := newProviderClient(upstream.URL, upstream.Client())
						if upstreamWS {
							client.enableWebSockets(ctx)
							defer client.websockets.close()
						}
						var handler http.Handler = responsesHandler(ctx, time.Second, client, nil, proxy)
						if downstreamWS {
							endpoint := responsesWebSocketHandler(ctx, time.Second, client, nil, proxy)
							defer endpoint.Close()
							handler = endpoint
						}
						downstream := httptest.NewServer(handler)
						defer downstream.Close()
						headers := serverMetadataHeaders(t, "turn", nil)
						metadata := codexTurnMetadata{RequestKind: "turn", Directories: map[string]jsonv1.RawMessage{t.TempDir(): nil}}
						if child {
							metadata.SubagentKind = threadSpawnSubagentKind
						}
						headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
						headers.Set(sessionIDHeader, "answer-stream-session")
						headers.Set("Authorization", "Bearer test")
						headers.Set(chatGPTAccountIDHeader, "account")
						request := serverRequest(t, func(fields map[string]any) { fields["stream"] = true })
						var next func() ([]byte, error)
						if downstreamWS {
							conn, _, err := websocket.Dial(ctx, downstream.URL, &websocket.DialOptions{HTTPHeader: headers})
							if err != nil {
								t.Fatal(err)
							}
							defer conn.CloseNow()
							var create map[string]jsonv1.RawMessage
							if err := json.Unmarshal(request.originalBody, &create); err != nil {
								t.Fatal(err)
							}
							create["type"] = mustMarshalJSON("response.create")
							socketWrite(t, ctx, conn, create)
							next = func() ([]byte, error) { _, payload, err := conn.Read(ctx); return payload, err }
						} else {
							req, err := http.NewRequestWithContext(ctx, http.MethodPost, downstream.URL, bytes.NewReader(request.originalBody))
							if err != nil {
								t.Fatal(err)
							}
							req.Header = headers
							response, err := downstream.Client().Do(req)
							if err != nil {
								t.Fatal(err)
							}
							defer response.Body.Close()
							if response.StatusCode != http.StatusOK {
								t.Fatalf("HTTP status %d", response.StatusCode)
							}
							reader := bufio.NewReader(response.Body)
							next = func() ([]byte, error) {
								var lines []string
								for {
									line, err := reader.ReadString('\n')
									if err != nil {
										return nil, err
									}
									if line == "\n" || line == "\r\n" {
										return ssePayload(lines), nil
									}
									lines = append(lines, line)
								}
							}
						}
						created, err := next()
						if err != nil || !bytes.Contains(created, []byte("response.created")) {
							t.Fatalf("created: %s, %v", created, err)
						}
						for index, expected := range events {
							actual, err := next()
							if err != nil || !bytes.Equal(actual, expected) {
								t.Fatalf("event %d before terminal: %s, %v", index, actual, err)
							}
							received <- struct{}{}
						}
						var output struct {
							Type     string `json:"type"`
							Response struct {
								Status string              `json:"status"`
								Output []jsonv1.RawMessage `json:"output"`
							} `json:"response"`
						}
						doneCount := 0
						for {
							actual, err := next()
							if err != nil {
								t.Fatal(err)
							}
							if err := json.Unmarshal(actual, &output); err != nil {
								t.Fatal(err)
							}
							if output.Type == "response.output_item.done" {
								doneCount++
								if bytes.Contains(actual, []byte(`"id":"answer"`)) {
									t.Fatal("terminal repeated the provider done event")
								}
							}
							if strings.HasPrefix(output.Type, "response.") && output.Type == "response."+status {
								break
							}
						}
						if output.Response.Status != status {
							t.Fatalf("terminal status %q", output.Response.Status)
						}
						if child && status == "completed" && doneCount != 1 {
							t.Fatalf("child result events %d", doneCount)
						}
						if status == "completed" && !bytes.Contains(mustMarshalJSON(output.Response.Output), []byte(`"id":"answer"`)) {
							t.Fatal("terminal history lost streamed answer")
						}
						if err := <-providerDone; err != nil {
							t.Fatal(err)
						}
						counts, available := proxy.usage.snapshot("thread-1")
						if !available || counts.tokenCounts != (tokenCounts{InputTokens: 20, UncachedInputTokens: 8, OutputTokens: 5, ReasoningTokens: 3}) {
							t.Fatalf("usage=%+v available=%v", counts, available)
						}
					})
				}
			}
		}
	}
}

func TestFinalAnswerStreamTerminalFinishesPartialLifecycle(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "child"}[child], func(t *testing.T) {
			transform, _, _, _ := newMekugiTestTransform(t)
			transform.subagentTurn = child
			answer := finalAnswerTestEvents(t, "final_answer")
			for _, event := range answer[:3] {
				visible, err := transform.TransformSSE(event)
				if err != nil || len(visible) != 1 || !bytes.Equal(visible[0], event) {
					t.Fatalf("partial answer did not stream: %s, %v", visible, err)
				}
				transform.ReleaseDelivery()
			}
			var done struct {
				Item jsonv1.RawMessage `json:"item"`
			}
			if err := json.Unmarshal(answer[len(answer)-1], &done); err != nil {
				t.Fatal(err)
			}
			terminal := mustTestJSON(t, map[string]any{"type": "response.completed", "response": map[string]any{"id": "partial", "status": "completed", "output": []jsonv1.RawMessage{done.Item}}})
			visible, err := transform.TransformSSE(terminal)
			if err != nil {
				t.Fatal(err)
			}
			defer transform.ReleaseDelivery()
			doneCount := 0
			for _, payload := range visible {
				var event struct {
					Type     string                       `json:"type"`
					Item     map[string]jsonv1.RawMessage `json:"item"`
					Response struct {
						Output []map[string]jsonv1.RawMessage `json:"output"`
					} `json:"response"`
				}
				if err := json.Unmarshal(payload, &event); err != nil {
					t.Fatal(err)
				}
				if event.Type == "response.output_item.done" && jsonString(event.Item, "id") == "answer" {
					doneCount++
				}
				if event.Type == "response.completed" && !slices.ContainsFunc(event.Response.Output, func(item map[string]jsonv1.RawMessage) bool { return jsonString(item, "id") == "answer" }) {
					t.Fatal("terminal lost partially streamed answer")
				}
			}
			if doneCount != 1 {
				t.Fatalf("item-done events %d", doneCount)
			}
		})
	}
}

func TestFinalAnswerStreamKeepsLateProgressInTerminalHistory(t *testing.T) {
	transform, proxy, _ := newSubagentCommentaryTestTransform(t, nil)
	transform.journalActive = false
	token := testRuntimeCommentaryCall(t, transform, "late-progress")
	transform.ReleaseDelivery()
	answer := finalAnswerTestEvents(t, "final_answer")
	for _, payload := range answer {
		visible, err := transform.TransformSSE(payload)
		if err != nil || len(visible) != 1 || !bytes.Equal(visible[0], payload) {
			t.Fatalf("answer did not stream: %s, %v", visible, err)
		}
	}
	proxy.commentary.publish(token, "Late tool progress.", false)
	var done struct {
		Item jsonv1.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(answer[len(answer)-1], &done); err != nil {
		t.Fatal(err)
	}
	visible, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": map[string]any{"id": "late", "status": "completed", "output": []jsonv1.RawMessage{done.Item}}}))
	if err != nil || len(visible) != 1 {
		t.Fatalf("late progress produced a trailing done event: %s, %v", visible, err)
	}
	var terminal struct {
		Response struct {
			Output []map[string]jsonv1.RawMessage `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal(visible[0], &terminal); err != nil {
		t.Fatal(err)
	}
	if len(terminal.Response.Output) != 2 || commentaryMessageText(terminal.Response.Output[0]) != "Late tool progress." || jsonString(terminal.Response.Output[1], "id") != "answer" {
		t.Fatalf("terminal lost late progress or displaced final: %s", visible)
	}
}
