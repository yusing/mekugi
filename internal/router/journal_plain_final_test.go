package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// A plain reply must not publish stale per-attempt usage before the actual finish.
func TestJournalPlainFinalContinuationUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, entries := range []bool{false, true} {
			t.Run(map[bool]string{false: "json", true: "sse"}[stream]+map[bool]string{false: "/empty", true: "/entry"}[entries], func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				answer := map[string]any{"id": "plain-answer", "type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Steering reply."}}}
				args := `{"op":"finish"}`
				if entries {
					args = `{"op":"finish","journal":[{"op":"add","text":"Work finished."}]}`
				}
				responses := []serverForwardResult{}
				for i, item := range []any{answer, journalFinishCall(args)} {
					body := map[string]any{
						"id": []string{"plain", "finish"}[i], "status": "completed", "output": []any{item},
						"usage": map[string]any{"input_tokens": 20, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 5, "output_tokens_details": map[string]any{"reasoning_tokens": 0}},
					}
					wire := string(mustMarshalJSON(body))
					if stream {
						wire = finalAnswerTestWire([][]byte{
							mustMarshalJSON(map[string]any{"type": "response.output_item.done", "item": item}),
							mustMarshalJSON(map[string]any{"type": "response.completed", "response": body}),
						})
					}
					response := serverHTTPResponse(wire)
					if stream {
						response.Header.Set("Content-Type", "text/event-stream")
					}
					responses = append(responses, serverForwardResult{response: response})
				}
				provider := &serverFakeProvider{results: responses}
				var output bytes.Buffer
				request := serverRequest(t, func(fields map[string]any) { fields["stream"] = stream })
				if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", nil), "session", provider, &output, nil, proxy, nil, nil); err != nil {
					t.Fatal(err)
				}
				if len(provider.forwarded) != 2 {
					t.Fatalf("requests = %d", len(provider.forwarded))
				}
				counts, ok := proxy.usage.snapshot("thread-1")
				if !ok || counts.InputTokens != 40 || counts.OutputTokens != 10 {
					t.Fatalf("usage = %+v, available = %v", counts, ok)
				}
				report := formatTokenUsageReport(counts)
				notices := 0
				for _, item := range journalFinishClientOutput(t, stream, output.Bytes()) {
					if strings.Contains(commentaryMessageText(item), "Tokens for this session") {
						notices++
						if commentaryMessageText(item) != report {
							t.Fatalf("stale completion usage: %s", commentaryMessageText(item))
						}
					}
				}
				if notices != 1 {
					t.Fatalf("usage notices = %d, want 1: %s", notices, output.Bytes())
				}
				if stream {
					delivered := 0
					for _, payload := range finalAnswerTestPayloads(output.String()) {
						var event struct {
							Type string                    `json:"type"`
							Item map[string]jsontext.Value `json:"item"`
						}
						if err := json.Unmarshal(payload, &event); err != nil {
							t.Fatal(err)
						}
						if event.Type == "response.output_item.done" && bytes.Contains(event.Item["content"], []byte("Tokens for this session")) {
							delivered++
						}
					}
					if delivered != 1 {
						t.Fatalf("streamed usage notices = %d", delivered)
					}
				}
			})
		}
	}
}

func TestJournalPlainFinalContinuationCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	provider := serverProviderFunc(func(_ context.Context, responseCtx context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
		calls++
		if calls == 1 {
			return journalFinishResponse(t, true, "completed", "full", map[string]any{"id": "answer", "type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Reply"}}}), nil
		}
		cancel()
		<-responseCtx.Done()
		return nil, responseCtx.Err()
	})
	var output bytes.Buffer
	request := serverRequest(t, func(fields map[string]any) { fields["stream"] = true })
	err := executeRequest(ctx, ctx, request, serverMetadataHeaders(t, "turn", nil), "session", provider, &output, nil, newManagedMekugiProxy(t), nil, nil)
	if err == nil || calls != 2 || strings.Contains(output.String(), `"type":"response.completed"`) {
		t.Fatalf("cancelled continuation: calls=%d err=%v output=%s", calls, err, output.Bytes())
	}
}

func TestJournalPlainFinalWebSocketContinuation(t *testing.T) {
	for _, compact := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "ctp2"}[compact], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			proxy := newManagedMekugiProxy(t)
			proxy.customizedInstructions = true
			proxy.compactModelProtocol = compact
			var codec *ctp2Codec
			if compact {
				codec = mustCTP2Codec(t)
			}
			headers := codexAuthHeaders()
			headers.Set(threadIDHeader, "thread-1")
			headers.Set(sessionIDHeader, "plain-final")
			headers.Set(codexTurnMetadataHeader, serverMetadataHeaders(t, "turn", nil).Get(codexTurnMetadataHeader))
			conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstream, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer upstream.CloseNow()
				upstream.SetReadLimit(upstreamJSONBufferBytes)
				for i, id := range []string{"plain", "finished"} {
					request, err := providerSocketRead(ctx, upstream)
					if err != nil {
						t.Error(err)
						return
					}
					if i == 1 && !bytes.Contains(request["input"], []byte("Steering reply.")) {
						t.Error("plain final absent from continuation history")
					}
					item := map[string]any{"id": "plain-answer", "type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Steering reply."}}}
					if i == 1 {
						item = journalFinishCall(`{"op":"finish"}`)
					}
					if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.output_item.done", "item": item}); err != nil {
						t.Error(err)
						return
					}
					if err := providerSocketWrite(ctx, upstream, map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{item}}}); err != nil {
						t.Error(err)
						return
					}
				}
				_, _, _ = upstream.Read(ctx)
			}), proxy, codec, headers)
			request := serverRequest(t, nil).fields
			request["type"] = mustMarshalJSON("response.create")
			socketWrite(t, ctx, conn, request)
			for {
				event := socketRead(t, ctx, conn)
				if jsonString(event, "type") == "error" {
					t.Fatalf("router error: %s", mustMarshalJSON(event))
				}
				if jsonString(event, "type") == "response.completed" {
					var response struct {
						ID string `json:"id"`
					}
					if err := json.Unmarshal(event["response"], &response); err != nil {
						t.Fatal(err)
					}
					if response.ID != "finished" {
						t.Fatalf("premature terminal: %s", response.ID)
					}
					break
				}
			}
		})
	}
}
