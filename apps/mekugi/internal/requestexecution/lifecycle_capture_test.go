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
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/modules/usage/accounting"
	"github.com/yusing/mekugi/providers/opencode"
)

// The preparer selects an admitted lifecycle result; the executor, coordination
// stop claim, gateway, provider and both capture boundaries remain real.
type lifecyclePreparation struct {
	localWait string
	reload    jsontext.Value
	usage     *accounting.Observation
}

func (p *lifecyclePreparation) Prepare(context.Context, *codexresponses.Request, Preparation) (Prepared, error) {
	return p, nil
}
func (p *lifecyclePreparation) LocalWaitItem() string         { return p.localWait }
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
		for _, kind := range []string{"local_wait", "context_reload", "provider", "usage_limit", "usage_limit_paused", "capacity", "overloaded", "capacity_stream", "third_party", "third_party_stream"} {
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
				preparation := new(lifecyclePreparation)
				thread, session, turn := "root", "root-session", "root-turn"
				thirdParty := strings.HasPrefix(kind, "third_party")
				capacity := strings.HasPrefix(kind, "capacity") || kind == "overloaded" || thirdParty
				limited := strings.HasPrefix(kind, "usage_limit") || capacity
				executor := Executor{Observations: preparation, Issues: testCriticalErrors()}
				if kind == "local_wait" || limited || kind == "provider" {
					retained, err := history.NewService(control.RecordKinds())
					check(err)
					store, err := retained.Open(ctx, t.TempDir())
					check(err)
					selected, err := store.SelectSession(ctx, nil, history.Limits{Total: history.DefaultStorageBytes}, thread)
					check(err)
					defer selected.Close()
					executor.ThreadControl = control.New()
					root := &control.Member{Thread: thread, Session: session, Task: "/root", Turn: turn, Generation: 1, Status: control.Working, HostRunning: true, AudienceAdmitted: true}
					if kind == "provider" {
						root.CapacityRetrySeconds, root.CapacityRetryGeneration = 30, 1
					}
					check(executor.ThreadControl.AdmitRoot(ctx, selected, root))
					if kind == "local_wait" {
						caller := control.Identity{Thread: thread, Session: session, Turn: turn, Call: "wait", Item: "exec"}
						spawn, err := executor.ThreadControl.ReserveSpawn(ctx, caller, control.SpawnRequest{Name: "worker", Message: "work", Settings: control.Settings{Workspace: t.TempDir()}})
						check(err)
						for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
							_, err := executor.ThreadControl.ClaimEffect(ctx, thread, spawn.ID)
							check(err)
							check(executor.ThreadControl.EffectAcknowledged(ctx, thread, spawn.ID, control.Receipt{Phase: phase, Thread: "child", Session: "child-session", Turn: "child-turn"}))
						}
						_, err = executor.ThreadControl.RegisterWait(ctx, caller, "", nil, nil, time.Now())
						check(err)
						check(executor.ThreadControl.Execution(ctx, thread, turn, caller.Item, "", true))
						preparation.localWait = caller.Item
					}
				} else if kind == "context_reload" {
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
					if thirdParty {
						w.Header().Set("Retry-After", "60")
					}
					if limited && kind != "capacity_stream" && kind != "third_party_stream" {
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
				if thirdParty {
					service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "fixture"}}).Services()[0]
					service.Endpoint = provider.URL + "/chat/completions"
					gateway.OpenCode = map[string]*inferencegateway.ThirdPartyClient{service.Prefix: {OpenCode: &service, HTTPClient: client, Diagnostics: ProviderDiagnostics()}}
					model = service.Prefix + ":glm-5.3-flash"
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
				server := httptest.NewServer(recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(finished)
					handler.ServeHTTP(w, r)
				})))
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
					if response.StatusCode != http.StatusOK || !bytes.Contains(payload, []byte("response.completed")) {
						t.Fatalf("lifecycle response: %d %s", response.StatusCode, payload)
					}
				} else {
					conn, _, err := websocket.Dial(ctx, server.URL+"/v1/responses", &websocket.DialOptions{HTTPHeader: headers})
					check(err)
					check(conn.Write(ctx, websocket.MessageText, []byte(body)))
					for {
						_, payload, err := conn.Read(ctx)
						check(err)
						if bytes.Contains(payload, []byte(`"type":"response.completed"`)) {
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
					t.Fatalf("local wait or rejection changed usage/rate: %+v", report)
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

				if limited {
					member, _ := executor.ThreadControl.Member(thread)
					if member.Stop == nil || member.Wait == nil || !member.Wait.UsageLimit || member.Status != control.Waiting || len(executor.Issues.Notices.Pending()) != 0 {
						t.Fatalf("usage limit did not become a silent logical stop: %+v notices=%+v", member, executor.Issues.Notices.Pending())
					}
					check(executor.ThreadControl.HostCompleted(ctx, thread, turn, control.Completed, control.HostFailure{}))
					if member.Wait.Capacity != capacity {
						t.Fatalf("capacity classification lost: %+v", member.Wait)
					}
					deadline := reset
					if member.Wait.Deadline != nil {
						deadline = *member.Wait.Deadline
					}
					wake, err := executor.ThreadControl.PrepareWake(ctx, thread, deadline.Add(-time.Nanosecond))
					check(err)
					if wake != nil {
						t.Fatal("usage limit resumed before provider reset")
					}
					wake, err = executor.ThreadControl.PrepareWake(ctx, thread, deadline)
					check(err)
					if (wake != nil) != (kind == "usage_limit" || capacity) {
						t.Fatalf("provider timing: wake=%+v", wake)
					}
					_, err = executor.ThreadControl.HumanInterrupt(ctx, thread, member.Stop.Wait)
					check(err)
					wake, err = executor.ThreadControl.PrepareWake(ctx, thread, reset.Add(time.Hour))
					check(err)
					if wake != nil {
						t.Fatal("canceled usage wait resumed")
					}
				}
				if kind == "provider" {
					member, _ := executor.ThreadControl.Member(thread)
					if member.CapacityRetrySeconds != 0 {
						t.Fatal("successful provider delivery did not reset capacity fallback")
					}
				}
				if snapshot.Requests.Logical != 1 || snapshot.Requests.Completed != 1 || int(snapshot.Requests.ProviderAttempts) != wantAttempts || snapshot.Capture.MissingProvider != 0 {
					t.Fatalf("lifecycle capture = %+v", snapshot)
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
