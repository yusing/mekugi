package router

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

func requireRoundThroughput(t *testing.T, usage *accounting.Service, thread string, rate float64, known bool) accounting.Report {
	t.Helper()
	report, _ := usage.Snapshot(thread)
	got, ok := report.RoundOutput.Throughput.Rate()
	if got != rate || ok != known {
		t.Fatalf("%s rate=(%g,%v), want (%g,%v); %+v", thread, got, ok, rate, known, report)
	}
	return report
}

func TestOutputThroughputRestoredThreadIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := usageStoreFixture(t, t.TempDir())
		usage := storedUsageFixture(t, store)
		first := usage.Observation("child", "child", "gpt-6-sol", "")
		first.Begin()
		time.Sleep(2 * time.Second)
		first.Observe(inference.Usage{TotalsKnown: true, OutputTokens: 100})
		usage.Close()
		restored := storedUsageFixture(t, store)
		restored.Restore("child", true)

		requireRoundThroughput(t, restored, "child", 50, true)
		requireRoundThroughput(t, restored, "fork", 0, false)
		u, _ := newAppServerTestUI()
		u.proxy = &mekugiProxy{usage: restored}
		child := activityPaneAgent{Name: "/root/child"}
		u.observeCost("child", &child)
		if outputThroughputLabel(child.OutputThroughput) != "50.0 tok/s" {
			t.Fatal(child)
		}
		time.Sleep(time.Second)
		interrupted := restored.Observation("child", "child", "gpt-6-sol", "")
		interrupted.Begin()
		restored.Close()
		again := storedUsageFixture(t, store)

		again.Restore("child", true)
		// Resume keeps the last measurement, without reviving a stopwatch.
		requireRoundThroughput(t, again, "child", 50, true)
		next := again.Observation("child", "child", "gpt-6-sol", "")
		next.Begin()
		time.Sleep(time.Second)
		next.Observe(inference.Usage{TotalsKnown: true, OutputTokens: 30})
		requireRoundThroughput(t, again, "child", 30, true)
	})
}

func TestOutputThroughputProviderRequestJSONAndSSE(t *testing.T) {
	for _, mode := range []string{"no-proxy", "thread", "module-absent"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/%s", stream, mode), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var proxy *mekugiProxy
					if mode != "no-proxy" {
						proxy = newManagedMekugiProxy(t)
						if mode == "module-absent" {
							proxy.usage = nil
							proxy.activity.usage = nil
						}
					}
					headers := serverMetadataHeaders(t, "turn", nil)
					headers.Set(sessionIDHeader, "transport-session")
					req := serverRequest(t, func(f map[string]any) { f["stream"] = stream; f["model"] = "gpt-6-sol" })
					terminal := `{"id":"tps-response","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":80,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":20}}}`
					wire := terminal
					if stream {
						wire = "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"custom_tool_call\",\"id\":\"tool\",\"call_id\":\"tool\",\"name\":\"unknown\",\"input\":\"" + strings.Repeat("inspect carefully ", 64) + "\"}}\n\n" +
							"data: {\"type\":\"response.output_text.delta\",\"output_index\":1,\"delta\":\"additional output\"}\n\n" +
							"data: {\"type\":\"response.completed\",\"response\":" + terminal + "}\n\n"
					}
					recorder, err := capturer.New(capturer.Config{Mode: "mekugi"})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = recorder.Close() })
					client := &http.Client{Transport: recorder.Transport(serverRoundTripper(func(_ *http.Request) (*http.Response, error) {
						time.Sleep(2 * time.Second)
						response := serverHTTPResponse(wire)
						if stream {
							response.Header.Set("Content-Type", "text/event-stream")
						}
						return response, nil
					}))}
					provider := serverProviderFunc(func(ctx, _ context.Context, body []byte, _ http.Header, _ string) (*http.Response, error) {
						request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://provider.invalid/responses", bytes.NewReader(body))
						if err != nil {
							return nil, err
						}
						return client.Do(request)
					})
					handler := recorder.Handler(http.HandlerFunc(responsesHandler(t.Context(), defaultRequestTimeout, provider, nil, proxy)))
					request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(mustTestJSON(t, req.Fields)))
					request.Header = headers
					out := &throughputSlowWriter{ResponseRecorder: httptest.NewRecorder()}
					handler.ServeHTTP(out, request)
					if out.Code != http.StatusOK {
						t.Fatalf("response=%d %s", out.Code, out.Body.String())
					}
					measurement := recorder.Snapshot().Usage.OutputThroughput
					if rate, known := measurement.Rate(); !known || rate != 40 {
						t.Fatalf("capture throughput=%+v", measurement)
					}
					if mode == "thread" {
						report := requireRoundThroughput(t, proxy.usage, "thread-1", 40, true)
						if report.OutputTokens != 80 || report.Roundtrips != 1 {
							t.Fatal(report)
						}
					}
				})
			})
		}
	}
}

// Delivery work between buffered events must not extend provider receipt time.
type throughputSlowWriter struct {
	*httptest.ResponseRecorder
}

func (w *throughputSlowWriter) Write(payload []byte) (int, error) {
	if bytes.Contains(payload, []byte("response.output_item.done")) {
		time.Sleep(time.Second)
	}
	return w.ResponseRecorder.Write(payload)
}

func TestOutputThroughputUIRefreshRetainsPreviousRound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, _ := newAppServerTestUI()
		u.session.start(u.thread, "")
		u.ensureShell()
		usage := newTestUsage(t)

		u.proxy = &mekugiProxy{usage: usage}
		first := usage.Observation(u.thread, u.thread, "gpt-6-sol", "")
		first.Begin()
		time.Sleep(time.Second)
		first.Observe(inference.Usage{TotalsKnown: true, OutputTokens: 30})
		u.applyObservedActivity()
		if outputThroughputLabel(u.session.agent("/root").OutputThroughput) != "30.0 tok/s" {
			t.Fatal("main did not refresh")
		}
		next := usage.Observation(u.thread, u.thread, "gpt-6-sol", "")
		next.Begin()
		u.applyObservedActivity()
		if got := outputThroughputLabel(u.session.agent("/root").OutputThroughput); got != "30.0 tok/s" {
			t.Fatalf("previous rate disappeared: %s", got)
		}
	})
}

func TestUISnapshotOutputThroughputStreamingRetainsMeasuredRate(t *testing.T) {
	for _, prior := range []bool{false, true} {
		t.Run(fmt.Sprintf("prior=%v", prior), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				u, _ := newAppServerTestUI()
				u.thread, u.proxy = "thread-1", proxy
				u.session.start(u.thread, "")
				u.ensureShell()
				var previous inference.OutputThroughput
				if prior {
					observation := proxy.usage.Observation(u.thread, "", "gpt-6.1-sol", "")
					observation.Begin()
					// Actual reported response: 620 of 883 output tokens were hidden reasoning.
					time.Sleep(20125689348 * time.Nanosecond)
					observation.Observe(inference.Usage{TotalsKnown: true, OutputTokens: 883, ReasoningTokens: 620})
					previous = observation.Throughput
				}
				r, w := io.Pipe()
				defer r.Close()
				defer w.Close()
				provider := serverProviderFunc(func(_, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
					response := serverHTTPResponse("")
					response.Header.Set("Content-Type", "text/event-stream")
					response.Body = r
					return response, nil
				})
				request := serverRequest(t, func(f map[string]any) { f["stream"] = true; f["model"] = "gpt-6.1-sol" })
				out := httptest.NewRecorder()
				httpRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(mustTestJSON(t, request.Fields)))
				httpRequest.Header = serverMetadataHeaders(t, "turn", nil)
				httpRequest.Header.Set(sessionIDHeader, "transport-session")
				done := make(chan struct{})
				go func() {
					responsesHandler(t.Context(), defaultRequestTimeout, provider, nil, proxy)(out, httpRequest)
					close(done)
				}()
				synctest.Wait()
				time.Sleep(2 * time.Second)
				for index, kind := range []string{"response.output_text.delta", "response.function_call_arguments.delta", "response.custom_tool_call_input.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta"} {
					event := mustTestJSON(t, map[string]any{"type": kind, "output_index": index, "delta": strings.Repeat("inspect carefully ", 64)})
					// Buffered deltas and streamed deltas must both leave the measured rate intact.
					if _, err := fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", event, event); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					u.applyObservedActivity()
					if got := u.session.agent("/root").OutputThroughput; got != previous {
						t.Fatalf("%s replaced measured rate: %+v, want %+v", kind, got, previous)
					}
					time.Sleep(250 * time.Millisecond)
				}
				root := u.session.agent("/root")
				u.agents.apply(activityPaneEvent{Kind: "agents", Agents: []activityPaneAgent{*root}})
				rows, _ := u.mainFrame(80, 10, 0)
				if prior {
					assertNativeUISnapshot(t, "output-throughput-live-composer", rows)
					assertNativeUISnapshot(t, "output-throughput-live-roster", u.agents.nativeRoster(80, 4, time.Now(), true))
				} else if strings.Contains(strings.Join(rows, "\n"), "tok/s") {
					t.Fatal("first response displayed TPS before provider usage")
				}
				terminal := `data: {"type":"response.completed","response":{"id":"rate","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":130,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":100}}}}` + "\n\n"
				if _, err := io.WriteString(w, terminal); err != nil {
					t.Fatal(err)
				}
				_ = w.Close()
				<-done
				u.applyObservedActivity()
				requireRoundThroughput(t, proxy.usage, u.thread, 40, true)
				if root.OutputThroughput.OutputTokens != 130 || out.Code != http.StatusOK {
					t.Fatalf("terminal measurement or delivery changed: %+v, %d", root.OutputThroughput, out.Code)
				}
				if state, err := copySSETransformed(io.Discard, strings.NewReader(out.Body.String()), nil, nil); err != nil || !state.Terminal() {
					t.Fatalf("downstream terminal missing: %v, %v", state, err)
				}
			})
		})
	}
}

func TestUISnapshotOutputThroughputComposerAndRoster(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for _, width := range []int{36, 80, 140} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.session.start(u.thread, "")
			u.ensureShell()
			u.view.painter.Theme = livediff.DarkTheme
			root := u.session.agent("/root")
			root.ContextKnown, root.ContextTokens, root.ContextWindow = true, 24000, 200000
			root.OutputThroughput = inference.OutputThroughput{OutputTokens: 83, DurationNanos: uint64(2 * time.Second), MeasuredRequests: 1}
			root.Final = true
			u.status, u.model = "Ready", "gpt-6-sol"
			rows, _ := u.mainFrame(width, 10, 0)
			assertNativeUISnapshot(t, fmt.Sprintf("output-throughput-composer-%d", width), rows)
			u.agents.apply(activityPaneEvent{Kind: "agents", Agents: []activityPaneAgent{*root,
				{Name: "/root/fast", OutputThroughput: inference.OutputThroughput{OutputTokens: 120, DurationNanos: uint64(time.Second), MeasuredRequests: 1}, ContextKnown: true, ContextTokens: 1000, ContextWindow: 200000},
				{Name: "/root/zero", OutputThroughput: inference.OutputThroughput{DurationNanos: uint64(time.Second), MeasuredRequests: 1}},
				{Name: "/root/absent"},
			}})
			assertNativeUISnapshot(t, fmt.Sprintf("output-throughput-roster-%d", width), u.agents.nativeRoster(width, 6, now, true))
		})
	}
}

func TestUISnapshotOutputThroughputRestoredRoster(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := usageStoreFixture(t, t.TempDir())
		usage := storedUsageFixture(t, store)
		measured := usage.Observation("child", "child", "gpt-6-sol", "")
		measured.Begin()
		time.Sleep(2 * time.Second)
		measured.Observe(inference.Usage{TotalsKnown: true, OutputTokens: 100})
		interrupted := usage.Observation("interrupted", "interrupted", "gpt-6-sol", "")
		interrupted.Begin()
		usage.Close()
		u := newAppServerSessionTestUI(t, t.TempDir())
		u.proxy = &mekugiProxy{usage: storedUsageFixture(t, store)}

		for _, thread := range []string{"child", "interrupted", "legacy"} {
			info := appServerThreadInfo{ID: thread, AgentNickname: thread, Turns: []appServerHistoryTurn{{ID: "old", Status: "completed"}}}
			u.session.registerThread(info)
			u.restoreActivityThread(info)
		}
		u.agents.apply(activityPaneEvent{Kind: "agents", Agents: u.session.agents})
		u.agents.painter.Theme = livediff.DarkTheme
		u.agents.selected = u.session.paths["child"]
		assertNativeUISnapshot(t, "output-throughput-restored-roster", u.agents.nativeRoster(120, 8, time.Now(), true))
	})
}
