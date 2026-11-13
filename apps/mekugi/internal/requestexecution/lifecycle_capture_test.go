package requestexecution

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

// The preparer selects a local source reload; the executor, gateway, provider
// and both capture boundaries remain real.
type lifecyclePreparation struct {
	reload jsontext.Value
	usage  *accounting.Observation
}

func (p *lifecyclePreparation) Prepare(context.Context, *codexresponses.Request, Preparation) (Prepared, error) {
	return p, nil
}
func (p *lifecyclePreparation) CompactReload() jsontext.Value { return p.reload }
func (*lifecyclePreparation) TransformJSON(body []byte) ([]byte, error) {
	return body, nil
}
func (*lifecyclePreparation) TransformSSE(body []byte) ([][]byte, error) {
	return [][]byte{body}, nil
}
func (*lifecyclePreparation) Finish(bool) error                     { return nil }
func (p *lifecyclePreparation) Usage() *accounting.Observation      { return p.usage }
func (p *lifecyclePreparation) ObserveUsage(counts inference.Usage) { p.usage.Observe(counts) }
func (*lifecyclePreparation) BindExecution(context.Context)         {}
func (*lifecyclePreparation) Close()                                {}

func TestLifecycleCaptureProviderDecision(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		for _, kind := range []string{"context_reload", "provider", "usage_limit", "usage_limit_paused", "capacity", "overloaded", "capacity_stream", "custom_provider", "custom_provider_stream"} {
			t.Run(transport+"/"+kind, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				check := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				directory := t.TempDir()
				capturePath := filepath.Join(directory, "capture.jsonl")
				recorder, err := new(transportcapture.Service).Open(transportcapture.Config{Output: capturePath, Mode: "mekugi"})
				check(err)
				defer recorder.Close()
				timingOwner, timingStore := retainTestTimings(t, recorder)
				preparation := new(lifecyclePreparation)
				thread, session, turn := "root", "root-session", "root-turn"
				customProvider := strings.HasPrefix(kind, "custom_provider")
				capacity := strings.HasPrefix(kind, "capacity") || kind == "overloaded" || customProvider
				limited := strings.HasPrefix(kind, "usage_limit") || capacity
				executor := Executor{Observations: preparation, Issues: testCriticalErrors()}
				if kind == "context_reload" {
					thread, session, turn = "child", "child-session", "child-turn"
					preparation.reload = jsontext.Value(`{"type":"custom_tool_call","id":"reload","call_id":"reload-call","name":"exec","input":"reload"}`)
				}
				providerCalls := make(chan struct{}, 2)
				terminal := []byte(`{"type":"response.completed","response":{"id":"provider-response","status":"completed","output":[],"usage":{"input_tokens":12,"output_tokens":3,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":1}}}}`)
				reset := time.Now().Add(time.Hour).Truncate(time.Second)
				rejection := map[string]any{"type": "usage_limit_reached"}
				if capacity {
					rejection = map[string]any{"code": "server_is_overloaded"}
					if kind == "overloaded" {
						rejection["message"] = "The servers are overloaded. Please try again later."
					}
				}
				if kind == "usage_limit" || kind == "capacity" {
					rejection["resets_at"] = reset.Unix()
				}
				if limited {
					terminal = responses.MustMarshalJSON(map[string]any{"type": "error", "status": 429, "error": rejection, "headers": map[string]any{"x-codex-primary-window-minutes": 15}})
				}
				if kind == "capacity_stream" {
					terminal = responses.MustMarshalJSON(map[string]any{"type": "response.failed", "response": map[string]any{"id": "rejected", "status": "failed", "error": rejection, "usage": map[string]any{"input_tokens": 1000, "output_tokens": 0}}})
				}
				usage := accounting.New()
				first := usage.Observation(thread, thread, "gpt-test", "")
				first.Begin()
				first.ReceivedAt = time.Now().Add(2 * time.Second)
				first.Observe(inference.Usage{TotalsKnown: true, OutputTokens: 80})
				baseline, _ := usage.Snapshot(thread)
				preparation.usage = usage.Observation(thread, thread, "gpt-test", "")

				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						conn, err := websocket.Accept(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.CloseNow()
						if _, _, err := conn.Read(ctx); err != nil {
							if kind == "provider" {
								t.Error(err)
							}
							return
						}
						providerCalls <- struct{}{}
						if err := conn.Write(ctx, websocket.MessageText, terminal); err != nil {
							t.Error(err)
						}
						_, _, _ = conn.Read(ctx)
						return
					}
					providerCalls <- struct{}{}
					if customProvider {
						w.Header().Set("Retry-After", "60")
					}
					if limited && kind != "capacity_stream" && kind != "custom_provider_stream" {
						w.Header().Set("Content-Type", "application/json")
						status := http.StatusTooManyRequests
						if capacity {
							status = http.StatusServiceUnavailable
						}
						w.WriteHeader(status)
						_, _ = w.Write(responses.MustMarshalJSON(map[string]any{"error": rejection}))
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write(append(append([]byte("data: "), terminal...), '\n', '\n'))
				}))
				defer provider.Close()
				client := provider.Client()
				client.Transport = recorder.Transport(client.Transport)
				gateway := inferencegateway.NewClient(provider.URL, client, ProviderDiagnostics())
				model := "gpt-test"
				if customProvider {
					gateway.Native = &inferencegateway.NativeProvider{ID: "fixture", BaseURL: provider.URL, HTTPClient: client}
				}
				executor.Provider = gateway
				headers := http.Header{}
				headers.Set("Authorization", "Bearer fixture")
				headers.Set("ChatGPT-Account-ID", "fixture-account")
				headers.Set(codexresponses.ThreadIDHeader, thread)
				headers.Set(codexresponses.RoutingSessionHeader, session)
				headers.Set(codexresponses.TurnMetadataHeader, `{"thread_id":"`+thread+`","turn_id":"`+turn+`","request_kind":"turn"}`)
				var handler http.Handler = inferencegateway.ResponsesHandler(ctx, 5*time.Second, executor.ExecuteHTTP)
				if transport == "websocket" {
					endpoint := inferencegateway.NewWebSocketEndpoint(ctx, 5*time.Second, executor.Provider.(*inferencegateway.Client), executor.ExecuteWebSocket)
					defer endpoint.Close()
					handler = endpoint
				}
				finished := make(chan struct{})
				capturedHandler := recorder.Handler(handler)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(finished)
					capturedHandler.ServeHTTP(w, r)
				}))
				defer server.Close()
				body := `{"type":"response.create","model":"` + model + `","stream":true,"input":[]}`
				if transport == "http" {
					request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", strings.NewReader(body))
					check(err)
					request.Header = headers
					response, err := server.Client().Do(request)
					check(err)
					payload, err := io.ReadAll(response.Body)
					check(err)
					check(response.Body.Close())
					if limited {
						wantStatus := http.StatusTooManyRequests
						if capacity {
							wantStatus = http.StatusServiceUnavailable
						}
						if kind == "capacity_stream" || kind == "custom_provider_stream" {
							wantStatus = http.StatusOK
						}
						if response.StatusCode != wantStatus || bytes.Contains(payload, []byte("response.completed")) || !bytes.Contains(payload, []byte("error")) {
							t.Fatalf("provider rejection changed: %d %s", response.StatusCode, payload)
						}
					} else if response.StatusCode != http.StatusOK || !bytes.Contains(payload, []byte("response.completed")) {
						t.Fatalf("lifecycle response: %d %s", response.StatusCode, payload)
					}
				} else {
					conn, _, err := websocket.Dial(ctx, server.URL+"/v1/responses", &websocket.DialOptions{HTTPHeader: headers})
					check(err)
					check(conn.Write(ctx, websocket.MessageText, []byte(body)))
					for {
						_, payload, err := conn.Read(ctx)
						check(err)
						completed := bytes.Contains(payload, []byte(`"type":"response.completed"`))
						rejected := bytes.Contains(payload, []byte(`"type":"error"`)) || bytes.Contains(payload, []byte(`"type":"response.failed"`))
						if limited && completed || !limited && rejected {
							t.Fatalf("provider outcome changed: %s", payload)
						}
						if kind == "custom_provider" && rejected {
							var event struct {
								Status int `json:"status"`
								Error  struct {
									Code string `json:"code"`
								} `json:"error"`
								Headers map[string]string `json:"headers"`
							}
							check(json.Unmarshal(payload, &event))
							if event.Status != http.StatusServiceUnavailable || event.Error.Code != "server_is_overloaded" || event.Headers["retry-after"] != "60" {
								t.Fatalf("native custom-provider rejection changed: %s", payload)
							}
						}
						if completed || rejected {
							break
						}
					}
					check(conn.CloseNow())
				}
				select {
				case <-finished:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				snapshot := recorder.Snapshot()
				report, _ := usage.Snapshot(thread)
				if kind == "provider" {
					if report.Roundtrips != 2 || report.OutputTokens != 83 || report.RoundOutput.Throughput.OutputTokens != 3 {
						t.Fatalf("provider accounting: %+v", report)
					}
				} else if report.Roundtrips != 1 || report.OutputTokens != 80 || report.RoundOutput.Throughput != baseline.RoundOutput.Throughput {
					t.Fatalf("local reload or rejection changed usage/rate: %+v", report)
				}
				wantAttempts := 0
				if kind == "provider" || limited {
					wantAttempts = 1
				}
				// OpenAI WebSockets establish a handshake even for local responses;
				// the captured provider attempt, rather than the dial, proves inference.
				if len(providerCalls) != wantAttempts {
					t.Fatalf("provider invocations = %d, want %d", len(providerCalls), wantAttempts)
				}

				wantCompleted, wantFailed, wantOutcome := uint64(1), uint64(0), "completed"
				if limited {
					wantCompleted, wantFailed, wantOutcome = 0, 1, "failed"
				}
				if snapshot.Requests.Logical != 1 || snapshot.Requests.Completed != wantCompleted || snapshot.Requests.Failed != wantFailed || int(snapshot.Requests.ProviderAttempts) != wantAttempts || snapshot.Capture.MissingProvider != 0 {
					t.Fatalf("lifecycle capture = %+v", snapshot)
				}
				check(recorder.Close())
				timing := inspectTestTimings(t, timingOwner, timingStore, thread, true)
				if timing.State != "observed_window" || len(timing.Requests) != 1 {
					t.Fatalf("retained lifecycle timing: %+v", timing)
				}
				requestTiming := timing.Requests[0]
				if requestTiming.Completed == nil || requestTiming.ToolResultReady != nil || requestTiming.Outcome != wantOutcome || len(requestTiming.Attempts) != wantAttempts {
					t.Fatalf("lifecycle phases: %+v", requestTiming)
				}
				for _, attempt := range requestTiming.Attempts {
					if attempt.Sent == nil || attempt.FirstOutput == nil || attempt.Completed == nil {
						t.Fatalf("provider phases: %+v", attempt)
					}
					if attempt.Sent.OffsetMillis < requestTiming.ForwardStarted.OffsetMillis || attempt.FirstOutput.OffsetMillis < attempt.Started.OffsetMillis || attempt.Completed.OffsetMillis > requestTiming.Completed.OffsetMillis {
						t.Fatalf("phase ordering: %+v", requestTiming)
					}
				}
				raw, err := os.ReadFile(capturePath)
				check(err)
				var front struct {
					Boundary         string `json:"boundary"`
					ProviderExpected *bool  `json:"provider_expected"`
				}
				for line := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
					check(json.Unmarshal(line, &front))
				}
				if front.Boundary != "codex" || (wantAttempts == 0 && (front.ProviderExpected == nil || *front.ProviderExpected)) || (wantAttempts != 0 && front.ProviderExpected != nil) {
					t.Fatalf("provider decision = %+v", front)
				}
				var metrics bytes.Buffer
				check(recorder.WriteMetrics(&metrics))
				if wantAttempts == 0 && !bytes.Contains(metrics.Bytes(), []byte(`"provider_attempts":[]`)) {
					t.Fatalf("missing empty provider attempt array: %s", &metrics)
				}
				// Optional artifacts let the independent family analyzer consume
				// these exact producer outputs without becoming a test dependency.
				if export := os.Getenv("MEKUGI_TEST_LIFECYCLE_EXPORT"); export != "" {
					destination := filepath.Join(export, transport+"-"+kind)
					check(os.MkdirAll(destination, 0700))
					check(os.WriteFile(filepath.Join(destination, "capture.jsonl"), raw, 0600))
					check(os.WriteFile(filepath.Join(destination, "metrics.json"), metrics.Bytes(), 0600))
				}
			})
		}
	}
}
