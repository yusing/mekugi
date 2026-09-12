package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestTokenUsageMentorAndManualSwitch(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			mentor := newMentorHandoff(true, true)
			headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
			// First request exhausts Mentor input budget; second uses configured Sol;
			// third is a manual switch to Luna. Each response uses the same stable thread.
			wants := []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-luna"}
			var out bytes.Buffer
			for i, model := range []string{"gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-luna"} {
				req := serverRequest(t, func(f map[string]any) { f["model"] = model; f["stream"] = stream })
				body := map[string]any{"id": fmt.Sprint("handoff-", i), "status": "completed", "output": []any{},
					"usage": map[string]any{"input_tokens": 100000, "input_tokens_details": map[string]any{"cached_tokens": 40000}, "output_tokens": 10000, "output_tokens_details": map[string]any{"reasoning_tokens": 5000}}}
				body["output"] = []any{map[string]any{"id": "answer", "type": "message", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Answer"}}}}
				wire := string(mustTestJSON(t, body))
				if stream {
					wire = finalAnswerTestWire(append(finalAnswerTestEvents(t, "final_answer"), mustTestJSON(t, map[string]any{"type": "response.completed", "response": body})))
				}
				response := serverHTTPResponse(wire)
				if stream {
					response.Header.Set("Content-Type", "text/event-stream")
				}
				provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
				out.Reset()
				if err := executeRequest(t.Context(), t.Context(), req, headers, fmt.Sprint("session-", i), provider, &out, nil, proxy, nil, mentor); err != nil {
					t.Fatal(err)
				}
				forwarded, err := parseResponsesRequest(provider.forwarded[0])
				if err != nil {
					t.Fatal(err)
				}
				if forwarded.model() != wants[i] {
					t.Fatalf("model=%s want=%s", forwarded.model(), wants[i])
				}
			}
			got, ok := proxy.usage.snapshot("thread-1")
			// Independent arithmetic: Astra 1.14 + Sol .456 + Luna .0248 = 1.6208.
			total := got.cost.uncachedInput + got.cost.cachedInput + got.cost.output
			if !ok || !got.cost.known || got.InputTokens != 300000 || got.UncachedInputTokens != 180000 || got.OutputTokens != 30000 || got.ReasoningTokens != 15000 || math.Abs(total-1.6208) > 1e-10 {
				t.Fatalf("report=%+v total=%v valid=%v", got, total, ok)
			}
			if !strings.Contains(out.String(), "| Total | — | $1.6208 |") {
				t.Fatalf("missing correct notice: %s", out.String())
			}
		})
	}
}

func TestTokenUsageWebSocketHandshakeRecovery(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			proxy.usage.observation("thread-1", "", "gpt-5.6-sol", "default").observe(tokenCounts{InputTokens: 100, UncachedInputTokens: 60, OutputTokens: 10, ReasoningTokens: 5})
			var handshakes atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if handshakes.Add(1) == 1 {
					http.Error(w, "handshake rejected", status)
					return
				}
				upstream, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer upstream.CloseNow()
				if _, err = providerSocketRead(ctx, upstream); err != nil {
					t.Error(err)
					return
				}
				for _, event := range finalAnswerTestEvents(t, "final_answer") {
					if err = upstream.Write(ctx, websocket.MessageText, event); err != nil {
						t.Error(err)
						return
					}
				}
				terminal := map[string]any{"type": "response.completed", "response": map[string]any{
					"id": "recovered", "status": "completed", "service_tier": "default", "output": []any{},
					"usage": map[string]any{"input_tokens": 100, "input_tokens_details": map[string]any{"cached_tokens": 40}, "output_tokens": 10, "output_tokens_details": map[string]any{"reasoning_tokens": 5}},
				}}
				if err = providerSocketWrite(ctx, upstream, terminal); err != nil {
					t.Error(err)
					return
				}
				_, _, _ = upstream.Read(ctx)
			}))
			defer provider.Close()
			endpoint := responsesWebSocketHandler(ctx, 10*time.Second, newProviderClient(provider.URL, provider.Client()), nil, proxy, nil, nil)
			defer endpoint.Close()
			router := httptest.NewServer(endpoint)
			defer router.Close()
			headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
			for key, values := range codexAuthHeaders() {
				headers[key] = values
			}
			headers.Set(sessionIDHeader, "reconnect-session")
			request := serverRequest(t, func(fields map[string]any) {
				fields["type"], fields["model"], fields["stream"], fields["service_tier"] = "response.create", "gpt-5.6-sol", true, "fast"
			})
			for attempt := range 2 {
				conn, _, err := websocket.Dial(ctx, router.URL, &websocket.DialOptions{HTTPHeader: headers})
				if err != nil {
					t.Fatal(err)
				}
				defer conn.CloseNow()
				conn.SetReadLimit(upstreamJSONBufferBytes)
				socketWrite(t, ctx, conn, request.fields)
				for {
					event := socketRead(t, ctx, conn)
					kind := jsonString(event, "type")
					if kind == "error" || kind == "response.completed" {
						if (kind == "error") != (attempt == 0) {
							t.Fatalf("unexpected terminal type=%s attempt=%d", kind, attempt)
						}
						break
					}
				}
				conn.CloseNow()
				got, valid := proxy.usage.snapshot("thread-1")
				if !valid || !got.cost.known || got.InputTokens != uint64(attempt+1)*100 {
					t.Fatalf("HTTP handshake rejection poisoned retained totals: %+v valid=%t", got, valid)
				}
			}
		})
	}
}

func TestTokenUsageRejectsIncompletePricing(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"input_tokens":null,"output_tokens":null}`, `{"input_tokens":100,"output_tokens":10}`} {
		t.Run(raw, func(t *testing.T) {
			counts, observed := usageFromResponsePayload([]byte(`{"usage":`+raw+`}`), false)
			cost := estimateTokenCost("gpt-6-astra", "", counts)
			if observed && cost.known {
				t.Errorf("incomplete telemetry accepted as complete priced usage: counts=%+v total=%v", counts, cost.uncachedInput+cost.cachedInput+cost.output)
			}
		})
	}
}

func TestTokenUsageGapSuppressesLaterReports(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, gap := range []string{"missing", "null", "partial", "invalid", "interrupted", "transport-error", "http-rejection", "failed-with-usage", "incomplete-with-usage", "compaction"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, gap), func(t *testing.T) {
				proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
				headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
				for step := range 3 {
					req := serverRequest(t, func(f map[string]any) { f["model"] = "gpt-6-astra"; f["stream"] = stream })
					currentHeaders := headers.Clone()
					body := map[string]any{
						"id": fmt.Sprint("gap-", step), "status": "completed",
						"output": []any{map[string]any{"id": "answer", "type": "message", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Answer"}}}},
						"usage":  map[string]any{"input_tokens": 100, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 10, "output_tokens_details": map[string]any{"reasoning_tokens": 0}},
					}
					wireStream := stream
					if step == 1 {
						switch gap {
						case "missing":
							delete(body, "usage")
						case "null":
							body["usage"] = nil
						case "partial":
							body["usage"] = map[string]any{"input_tokens": 100, "output_tokens": 10}
						case "invalid":
							body["usage"] = map[string]any{"input_tokens": -1}
						case "failed-with-usage":
							body["status"] = "failed"
						case "incomplete-with-usage":
							body["status"] = "incomplete"
						case "compaction":
							currentHeaders = serverCompactionMetadataHeaders(t)
							currentHeaders.Set(threadIDHeader, "thread-1")
							req = serverRequest(t, func(f map[string]any) {
								f["model"] = "gpt-6-astra"
								f["stream"] = true
								delete(f, "tools")
								f["parallel_tool_calls"] = false
								f["input"] = []any{map[string]any{"role": "user", "content": "compact"}}
							})
							wireStream = true
							body["output"] = []any{}
							delete(body, "usage")
						}
					}
					wire := string(mustTestJSON(t, body))
					if wireStream {
						wire = finalAnswerTestWire(append(finalAnswerTestEvents(t, "final_answer"), mustTestJSON(t, map[string]any{"type": "response." + body["status"].(string), "response": body})))
					}
					if step == 1 && gap == "interrupted" {
						if wireStream {
							wire = finalAnswerTestWire(finalAnswerTestEvents(t, "final_answer"))
						} else {
							wire = `{"status":`
						}
					}
					response := serverHTTPResponse(wire)
					if wireStream {
						response.Header.Set("Content-Type", "text/event-stream")
					}
					if step == 1 && gap == "http-rejection" {
						response.StatusCode = http.StatusBadRequest
					}
					result := serverForwardResult{response: response}
					if step == 1 && gap == "transport-error" {
						result = serverForwardResult{err: fmt.Errorf("connection lost after forwarding")}
					}
					provider := &serverFakeProvider{results: []serverForwardResult{result}}
					var out bytes.Buffer
					err := executeRequest(t.Context(), t.Context(), req, currentHeaders, "session", provider, &out, nil, proxy, nil, nil)
					wantErr := step == 1 && (gap == "interrupted" || gap == "transport-error")
					if (err != nil) != wantErr {
						t.Fatalf("step=%d err=%v", step, err)
					}
					if step == 2 {
						wantReport := gap == "http-rejection" || gap == "failed-with-usage" || gap == "incomplete-with-usage"
						if strings.Contains(out.String(), "Tokens:") != wantReport {
							t.Fatalf("unexpected final report: %s", out.String())
						}
						got, valid := proxy.usage.snapshot("thread-1")
						if valid != wantReport {
							t.Fatalf("aggregate validity=%t", valid)
						}
						if wantReport {
							wantInput := uint64(300)
							if gap == "http-rejection" {
								wantInput = 200
							}
							if got.InputTokens != wantInput {
								t.Fatalf("input=%d want=%d", got.InputTokens, wantInput)
							}
						}
					}
				}
			})
		}
	}
}

func TestTokenUsageAutomaticSuccessorAtHandoff(t *testing.T) {
	for _, tc := range []struct {
		configured, leader, requestedTier, servedTier string
		mentorCost, configuredCost                    float64
	}{
		{"gpt-5.6-terra", "gpt-5.6-sol", "default", "default", .456, .248},
		{"gpt-5.6-sol", "gpt-6-astra", "default", "default", 1.14, .456},
		{"gpt-5.6-terra", "gpt-5.6-sol", "fast", "priority", .912, .496},
		{"gpt-5.6-sol", "gpt-6-astra", "priority", "fast", 2.28, .912},
	} {
		t.Run(tc.configured+"/"+tc.requestedTier, func(t *testing.T) {
			testTokenUsageAutomaticSuccessor(t, tc.configured, tc.leader, tc.requestedTier, tc.servedTier, tc.mentorCost, tc.configuredCost)
		})
	}
}

func testTokenUsageAutomaticSuccessor(t *testing.T, configured, leader, requestedTier, servedTier string, mentorCost, configuredCost float64) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	mentor := newMentorHandoff(true, true)
	usage := map[string]any{"input_tokens": 100000, "input_tokens_details": map[string]any{"cached_tokens": 40000}, "output_tokens": 10000, "output_tokens_details": map[string]any{"reasoning_tokens": 5000}}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.CloseNow()
		create, err := providerSocketRead(ctx, upstream)
		if err != nil {
			t.Error(err)
			return
		}
		if jsonString(create, "model") != leader {
			t.Errorf("provider model=%s", jsonString(create, "model"))
		}
		if err = providerSocketWrite(ctx, upstream, socketEvent("response.created", "parent")); err != nil {
			t.Error(err)
			return
		}
		if _, err = providerSocketRead(ctx, upstream); err != nil {
			t.Error(err)
			return
		}
		events := []any{
			map[string]any{"type": "response.steer.accepted", "steer": map[string]any{"id": "s1", "previous_response_id": "parent"}},
			map[string]any{"type": "response.incomplete", "response": map[string]any{"id": "parent", "model": leader, "service_tier": servedTier, "status": "incomplete", "incomplete_details": map[string]any{"reason": "steered"}, "output": []any{}, "usage": usage}},
			map[string]any{"type": "response.created", "response": map[string]any{"id": "successor", "previous_response_id": "parent", "model": leader, "service_tier": servedTier, "status": "in_progress", "output": []any{}}},
		}
		for _, e := range events {
			if err = providerSocketWrite(ctx, upstream, e); err != nil {
				t.Error(err)
				return
			}
		}
		for _, e := range finalAnswerTestEvents(t, "final_answer") {
			if err = upstream.Write(ctx, websocket.MessageText, e); err != nil {
				t.Error(err)
				return
			}
		}
		if err = providerSocketWrite(ctx, upstream, map[string]any{"type": "response.completed", "response": map[string]any{"id": "successor", "model": leader, "service_tier": servedTier, "status": "completed", "output": []any{}, "usage": usage}}); err != nil {
			t.Error(err)
			return
		}

		// The next provider create must be the explicit client request, not one
		// fabricated for the automatic successor. Only it may change the model.
		next, err := providerSocketRead(ctx, upstream)
		if err != nil {
			t.Error(err)
			return
		}
		if jsonString(next, "model") != configured || jsonString(next, "previous_response_id") != "successor" || jsonString(next, "service_tier") != requestedTier {
			t.Errorf("next model=%s parent=%s tier=%s", jsonString(next, "model"), jsonString(next, "previous_response_id"), jsonString(next, "service_tier"))
		}
		for _, e := range finalAnswerTestEvents(t, "final_answer") {
			if err = upstream.Write(ctx, websocket.MessageText, e); err != nil {
				t.Error(err)
				return
			}
		}
		if err = providerSocketWrite(ctx, upstream, map[string]any{"type": "response.completed", "response": map[string]any{"id": "last", "model": configured, "service_tier": servedTier, "status": "completed", "output": []any{}, "usage": usage}}); err != nil {
			t.Error(err)
			return
		}
		_, _, _ = upstream.Read(ctx)
	}))
	defer provider.Close()
	endpoint := responsesWebSocketHandler(ctx, 10*time.Second, newProviderClient(provider.URL, provider.Client()), nil, proxy, nil, mentor)
	defer endpoint.Close()
	router := httptest.NewServer(endpoint)
	defer router.Close()
	headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
	for k, v := range codexAuthHeaders() {
		headers[k] = v
	}
	headers.Set(sessionIDHeader, "handoff-socket")
	conn, _, err := websocket.Dial(ctx, router.URL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(upstreamJSONBufferBytes)
	request := serverRequest(t, func(f map[string]any) {
		f["model"] = configured
		f["service_tier"] = requestedTier
		f["stream"] = true
		f["type"] = "response.create"
	})
	socketWrite(t, ctx, conn, request.fields)
	if event := socketRead(t, ctx, conn); jsonString(event, "type") != "response.created" {
		t.Fatalf("first=%s", mustMarshalJSON(event))
	}
	socketWrite(t, ctx, conn, map[string]any{"type": "response.steer", "previous_response_id": "parent", "input": "change direction"})
	for {
		event := socketRead(t, ctx, conn)
		if jsonString(event, "type") == "error" {
			t.Fatalf("successor error=%s", mustMarshalJSON(event))
		}
		if jsonString(event, "type") == "response.completed" {
			break
		}
	}
	got, ok := proxy.usage.snapshot("thread-1")
	total := got.cost.uncachedInput + got.cost.cachedInput + got.cost.output
	if !ok || !got.cost.known || got.InputTokens != 200000 || math.Abs(total-2*mentorCost) > 1e-10 {
		t.Fatalf("successor repriced unsent model: report=%+v total=%v want=%v", got, total, 2*mentorCost)
	}
	request.fields["previous_response_id"] = mustMarshalJSON("successor")
	request.fields["input"] = mustMarshalJSON([]any{map[string]any{"role": "user", "content": "next task"}})
	socketWrite(t, ctx, conn, request.fields)
	for {
		event := socketRead(t, ctx, conn)
		if jsonString(event, "type") == "error" {
			t.Fatalf("explicit successor error=%s", mustMarshalJSON(event))
		}
		if jsonString(event, "type") == "response.completed" {
			break
		}
	}
	got, ok = proxy.usage.snapshot("thread-1")
	total = got.cost.uncachedInput + got.cost.cachedInput + got.cost.output
	if !ok || !got.cost.known || got.InputTokens != 300000 || math.Abs(total-(2*mentorCost+configuredCost)) > 1e-10 {
		t.Fatalf("explicit successor failed to change price: report=%+v total=%v", got, total)
	}
}
