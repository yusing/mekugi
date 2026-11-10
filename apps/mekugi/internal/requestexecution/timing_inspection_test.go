package requestexecution

import (
	"bytes"
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"fmt"
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
	"github.com/yusing/mekugi/apps/mekugi/internal/inspectioncli"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessioninspection/codexbinding"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

type inspectedTimings struct {
	State    string                           `json:"state"`
	Thread   string                           `json:"thread_id"`
	Requests []transportcapture.RequestTiming `json:"requests"`
}

func retainTestTimings(t *testing.T, recorder *transportcapture.Recorder) (*history.Service, *history.Store) {
	t.Helper()
	owner, err := history.NewService([]recordkind.Spec{transportcapture.TimingRecord})
	if err != nil {
		t.Fatal(err)
	}
	store, err := owner.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recorder.RetainTimings(store, history.Limits{}, func(thread string, err error) { t.Errorf("retain %s: %v", thread, err) })
	return owner, store
}

func inspectTestTimings(t *testing.T, owner *history.Service, store *history.Store, thread string, transport bool) inspectedTimings {
	t.Helper()
	metrics := new(evidence.Service)
	contributions := []inspection.Contribution{{NewAX: func() inspection.AXCollector { return metrics.NewInspection(nil) }}}
	if transport {
		contributions = append(contributions, inspection.Contribution{RequestTiming: new(transportcapture.Service).InspectTiming})
	}
	inspector, err := query.New(owner, contributions)
	if err != nil {
		t.Fatal(err)
	}
	if err := codexbinding.Codex.Register(new(binding.Registry), inspector, nil); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	data, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": thread, "cwd": t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollout, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := inspectioncli.RunSessionInspection(inspector, nil, t.Context(), []string{"--session", rollout, "--replay-dir", store.Directory(), "--ax", "--offset", "100"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("inspection code=%d error=%s", code, &stderr)
	}
	var report struct {
		AX struct {
			Timing inspectedTimings `json:"request_timing"`
		} `json:"ax"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "private-prompt") || strings.Contains(stdout.String(), "fixture-secret") {
		t.Fatalf("private content in timing inspection: %s", &stdout)
	}
	return report.AX.Timing
}

// Exercise both gateways, the execution finalizer, retained store and CLI.
// Codex retries are new requests, not guessed continuations of a previous one.
func TestRequestTimingFailureRetryAndCancellation(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		for _, scenario := range []string{"retry", "cancel", "missing_terminal"} {
			t.Run(transport+"/"+scenario, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				recorder, err := new(transportcapture.Service).Open(transportcapture.Config{})
				if err != nil {
					t.Fatal(err)
				}
				defer recorder.Close()
				owner, store := retainTestTimings(t, recorder)
				providerCalls := 0
				releaseProvider := make(chan struct{})
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var conn *websocket.Conn
					if transport == "websocket" {
						var err error
						conn, err = websocket.Accept(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.CloseNow()
						if _, _, err := conn.Read(ctx); err != nil {
							t.Error(err)
							return
						}
					}
					providerCalls++
					if scenario == "cancel" {
						cancel()
						// Keep the peer open until cancellation has settled. A peer
						// close racing shutdown is a different transport failure.
						<-releaseProvider
						return
					}
					if scenario == "retry" && providerCalls == 1 {
						if conn != nil {
							_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.failed","response":{"id":"resp-test","status":"failed","error":{"message":"fixture-secret"}}}`))
							return
						}
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = io.WriteString(w, `{"error":{"message":"fixture-secret"}}`)
						return
					}
					payload := `{"type":"response.completed","response":{"id":"resp-test","status":"completed","output":[]}}`
					if scenario == "missing_terminal" {
						payload = `{"type":"response.created","response":{"id":"resp-test"}}`
					}
					if conn != nil {
						if err := conn.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
							t.Error(err)
						}
						if scenario != "missing_terminal" {
							_, _, _ = conn.Read(ctx)
						}
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+payload+"\n\n")
				}))
				defer func() { close(releaseProvider); provider.Close() }()
				client := provider.Client()
				client.Transport = recorder.Transport(client.Transport)
				gateway := inferencegateway.NewClient(provider.URL, client, ProviderDiagnostics())
				executor := Executor{Provider: gateway, Issues: testCriticalErrors()}
				var handler http.Handler = inferencegateway.ResponsesHandler(ctx, time.Second, executor.ExecuteHTTP)
				if transport == "websocket" {
					endpoint := inferencegateway.NewWebSocketEndpoint(ctx, time.Second, gateway, executor.ExecuteWebSocket)
					defer endpoint.Close()
					handler = endpoint
				}
				handler = recorder.Handler(handler)
				finished := make(chan struct{}, 2)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					handler.ServeHTTP(w, r)
					finished <- struct{}{}
				}))
				defer server.Close()
				count := 1
				if scenario == "retry" {
					count = 2
				}
				for range count {
					body := `{"type":"response.create","model":"test","stream":true,"input":"private-prompt"}`
					request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/responses", strings.NewReader(body))
					request.Header.Set("Authorization", "Bearer fixture-secret")
					request.Header.Set("ChatGPT-Account-ID", "fixture-secret")
					request.Header.Set(codexresponses.ThreadIDHeader, "child-thread")
					if transport == "http" {
						handler.ServeHTTP(httptest.NewRecorder(), request)
					} else {
						conn, _, err := websocket.Dial(ctx, server.URL+"/v1/responses", &websocket.DialOptions{HTTPHeader: request.Header})
						if err != nil {
							t.Fatal(err)
						}
						if err := conn.Write(ctx, websocket.MessageText, []byte(body)); err != nil && scenario != "cancel" {
							t.Fatal(err)
						}
						for {
							_, payload, err := conn.Read(ctx)
							if err != nil || bytes.Contains(payload, []byte(`"type":"response.completed"`)) {
								break
							}
						}
						_ = conn.CloseNow()
						select {
						case <-finished:
						case <-time.After(2 * time.Second):
							t.Fatal("WebSocket request did not finish")
						}
					}
				}
				if err := recorder.Close(); err != nil {
					t.Fatal(err)
				}
				report := inspectTestTimings(t, owner, store, "child-thread", true)
				if report.State != "observed_window" || report.Thread != "child-thread" || len(report.Requests) != count {
					t.Fatalf("timings: %+v", report)
				}
				for i, request := range report.Requests {
					if request.CaptureID == "" || request.ToolResultReady != nil || request.ForwardStarted == nil || request.Completed == nil || len(request.Attempts) != 1 {
						t.Fatalf("request: %+v", request)
					}
					attempt := request.Attempts[0]
					if scenario != "cancel" && attempt.Sent == nil || attempt.Completed == nil || attempt.Transport != transport {
						t.Fatalf("attempt: %+v", attempt)
					}
					if scenario == "cancel" {
						if request.Outcome != "canceled_before_response" || attempt.FirstOutput != nil {
							t.Fatalf("cancel timing: %+v", request)
						}
					} else if attempt.FirstOutput == nil {
						t.Fatalf("missing first output: %+v", attempt)
					}
					if scenario == "retry" {
						want := "failed"
						if i == 1 {
							want = "completed"
						}
						if request.Outcome != want {
							t.Fatalf("retry outcome: %s, want %s", request.Outcome, want)
						}
					}
					if scenario == "missing_terminal" && request.Outcome != "failed" {
						t.Fatalf("missing terminal: %+v", request)
					}
				}
				if count == 2 && report.Requests[0].CaptureID == report.Requests[1].CaptureID {
					t.Fatal("retry reused request identity")
				}
				if absent := inspectTestTimings(t, owner, store, "unrelated-thread", true); absent.State != "unavailable" || len(absent.Requests) != 0 {
					t.Fatalf("cross-thread evidence: %+v", absent)
				}
				if absent := inspectTestTimings(t, owner, store, "child-thread", false); absent.State != "unavailable" || len(absent.Requests) != 0 {
					t.Fatalf("unselected transport: %+v", absent)
				}
			})
		}
	}
}

func TestRequestTimingInspectionFailurePreservesAX(t *testing.T) {
	for _, scenario := range []string{"historical", "malformed", "nonregular"} {
		t.Run(scenario, func(t *testing.T) {
			owner, err := history.NewService(nil)
			if err != nil {
				t.Fatal(err)
			}
			workspace, replay := t.TempDir(), t.TempDir()
			const patch = "*** Begin Patch\n*** Add File: changed.txt\n+text\n*** End Patch\n"
			// Retained stock patch evidence is independent of request timing.
			for _, record := range []legacycall.Record{
				{Version: 1, Workspace: workspace, CallID: "patch", History: legacycall.History{
					ToolName: "apply_patch", CarrierName: "apply_patch", CarrierKind: "custom", CarrierPayload: patch, Script: patch,
					NativePatches: []legacycall.NativePatch{{Input: patch}},
				}},
				{Version: 1, Workspace: workspace, CallID: legacycall.PatchCallID("patch", 0), History: legacycall.History{
					ToolName: "apply_patch", Script: patch, CorrelationID: "patch\x000", AlreadySatisfied: true,
				}},
			} {
				data, err := json.Marshal(&record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(replay, legacycall.Name(workspace, record.CallID, false)), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var source bytes.Buffer
			for _, record := range []any{
				map[string]any{"type": "session_meta", "payload": map[string]any{"id": "thread", "cwd": workspace}},
				map[string]any{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "call_id": "patch", "name": "apply_patch", "input": patch}},
				map[string]any{"type": "event_msg", "payload": map[string]any{
					"type": "item_completed", "started_at_ms": 1000, "completed_at_ms": 2500,
					"item": map[string]any{"type": "CommandExecution", "id": "command", "exit_code": 0},
				}},
			} {
				data, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				source.Write(data)
				source.WriteByte('\n')
			}
			rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
			if err := os.WriteFile(rollout, source.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			timingPath := filepath.Join(replay, fmt.Sprintf("request-timing-%x.json", sha256.Sum256([]byte("thread"))))
			switch scenario {
			case "malformed":
				if err := os.WriteFile(timingPath, []byte(`{"version":1,"thread_id":"thread","requests":[{"outcome":"private-error"}]}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "nonregular":
				if err := os.Mkdir(timingPath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			inspector, err := query.New(owner, []inspection.Contribution{{
				NewAX:         func() inspection.AXCollector { return new(evidence.Service).NewInspection(nil) },
				RequestTiming: new(transportcapture.Service).InspectTiming,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := codexbinding.Codex.Register(new(binding.Registry), inspector, nil); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := inspectioncli.RunSessionInspection(inspector, nil, t.Context(), []string{"--session", rollout, "--replay-dir", replay, "--ax"}, &stdout, &stderr)
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("optional timing suppressed inspection: code=%d error=%s", code, &stderr)
			}
			var report struct {
				TotalCalls int                       `json:"total_calls"`
				Calls      []query.CallResult        `json:"calls"`
				AX         evidence.InspectionReport `json:"ax"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.TotalCalls != 1 || len(report.Calls) != 1 || report.AX.Commands.Completed != 1 || report.AX.Commands.DurationMS != 1500 ||
				report.AX.Edits.Calls != 1 || report.AX.Edits.EmittedBytes != uint64(len(patch)) || report.AX.Edits.Unconfirmed != 0 {
				t.Fatalf("unrelated call/AX evidence lost: %s", &stdout)
			}
			var timing struct {
				State    string                           `json:"state"`
				Reason   string                           `json:"reason"`
				Requests []transportcapture.RequestTiming `json:"requests"`
			}
			if err := json.Unmarshal(report.AX.RequestTiming, &timing); err != nil {
				t.Fatal(err)
			}
			wantState := "error"
			if scenario == "historical" {
				wantState = "unavailable"
			}
			if timing.State != wantState || len(timing.Requests) != 0 || scenario != "historical" && timing.Reason == "" || bytes.Contains(stdout.Bytes(), []byte("private-error")) {
				t.Fatalf("timing failure/absence distinction: %s", report.AX.RequestTiming)
			}
		})
	}
}
