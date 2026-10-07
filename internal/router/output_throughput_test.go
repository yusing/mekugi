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
	"github.com/yusing/mekugi/internal/livediff"
)

func requireRoundThroughput(t *testing.T, usage *threadUsage, thread string, rate float64, known bool) tokenUsageReport {
	t.Helper()
	report, _ := usage.snapshot(thread)
	got, ok := report.roundOutput.Throughput.Rate()
	if got != rate || ok != known {
		t.Fatalf("%s rate=(%g,%v), want (%g,%v); %+v", thread, got, ok, rate, known, report)
	}
	return report
}

func TestOutputThroughputCurrentRound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		usage := newThreadUsage()
		t.Cleanup(usage.close)
		first := usage.observation("main", "main", "gpt-6-sol", "")
		first.begin()
		requireRoundThroughput(t, usage, "main", 0, false)
		time.Sleep(2 * time.Second)
		first.observe(tokenCounts{TotalsKnown: true, OutputTokens: 80})
		requireRoundThroughput(t, usage, "main", 40, true)
		first.observe(tokenCounts{TotalsKnown: true, OutputTokens: 999})
		first.finish()
		if report := requireRoundThroughput(t, usage, "main", 40, true); report.roundtrips != 1 {
			t.Fatal(report)
		}
		// Tool time between requests does not dilute the next provider round.
		time.Sleep(time.Minute)
		zero := usage.observation("main", "main", "gpt-6-luna", "")
		zero.begin()
		requireRoundThroughput(t, usage, "main", 40, true)
		time.Sleep(time.Second)
		zero.observe(tokenCounts{TotalsKnown: true})
		requireRoundThroughput(t, usage, "main", 0, true)
		time.Sleep(time.Second)
		gap := usage.observation("main", "main", "gpt-6-sol", "")
		gap.begin()
		time.Sleep(time.Second)
		gap.finish()
		requireRoundThroughput(t, usage, "main", 0, true)
	})
}

func TestOutputThroughputConcurrentRoundOrdering(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		usage := newThreadUsage()
		t.Cleanup(usage.close)
		old := usage.observation("main", "main", "gpt-6-sol", "")
		old.begin()
		time.Sleep(time.Second)
		next := usage.observation("main", "main", "gpt-6-sol", "")
		next.begin()
		time.Sleep(time.Second)
		next.observe(tokenCounts{TotalsKnown: true, OutputTokens: 60})
		requireRoundThroughput(t, usage, "main", 60, true)
		time.Sleep(time.Second)
		old.observe(tokenCounts{TotalsKnown: true, OutputTokens: 30})
		requireRoundThroughput(t, usage, "main", 60, true)
		// The older request still keeps its own paired measurement for capture.
		if rate, known := old.throughput.Rate(); !known || rate != 10 {
			t.Fatalf("older capture rate=%g,%v", rate, known)
		}
	})
}

func TestOutputThroughputRestoredThreadIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := usageStoreFixture(t, t.TempDir())
		usage := storedUsageFixture(store)
		first := usage.observation("child", "child", "gpt-6-sol", "")
		first.begin()
		time.Sleep(2 * time.Second)
		first.observe(tokenCounts{TotalsKnown: true, OutputTokens: 100})
		usage.close()
		restored := storedUsageFixture(store)
		restored.restore("child", true)
		t.Cleanup(restored.close)
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
		interrupted := restored.observation("child", "child", "gpt-6-sol", "")
		interrupted.begin()
		restored.close()
		again := storedUsageFixture(store)
		t.Cleanup(again.close)
		again.restore("child", true)
		// Resume keeps the last measurement, without reviving a stopwatch.
		requireRoundThroughput(t, again, "child", 50, true)
		next := again.observation("child", "child", "gpt-6-sol", "")
		next.begin()
		time.Sleep(time.Second)
		next.observe(tokenCounts{TotalsKnown: true, OutputTokens: 30})
		requireRoundThroughput(t, again, "child", 30, true)
	})
}

func TestOutputThroughputProviderRequestJSONAndSSE(t *testing.T) {
	for _, recordThread := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/thread=%v", stream, recordThread), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var proxy *mekugiProxy
					if recordThread {
						proxy = newManagedMekugiProxy(t)
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
					request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(mustTestJSON(t, req.fields)))
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
					if recordThread {
						report := requireRoundThroughput(t, proxy.usage, "thread-1", 40, true)
						if report.OutputTokens != 80 || report.roundtrips != 1 {
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
		usage := newThreadUsage()
		t.Cleanup(usage.close)
		u.proxy = &mekugiProxy{usage: usage}
		first := usage.observation(u.thread, u.thread, "gpt-6-sol", "")
		first.begin()
		time.Sleep(time.Second)
		first.observe(tokenCounts{TotalsKnown: true, OutputTokens: 30})
		u.applyObservedActivity()
		if outputThroughputLabel(u.session.agent("/root").OutputThroughput) != "30.0 tok/s" {
			t.Fatal("main did not refresh")
		}
		next := usage.observation(u.thread, u.thread, "gpt-6-sol", "")
		next.begin()
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
				var previous capturer.OutputThroughput
				if prior {
					observation := proxy.usage.observation(u.thread, "", "gpt-6.1-sol", "")
					observation.begin()
					// Actual reported response: 620 of 883 output tokens were hidden reasoning.
					time.Sleep(20125689348 * time.Nanosecond)
					observation.observe(tokenCounts{TotalsKnown: true, OutputTokens: 883, ReasoningTokens: 620})
					previous = observation.throughput
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
				httpRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(mustTestJSON(t, request.fields)))
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
				if state, err := copySSETransformed(io.Discard, strings.NewReader(out.Body.String()), nil, nil); err != nil || !isResponseTerminal(state) {
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
			root.OutputThroughput = capturer.OutputThroughput{OutputTokens: 83, DurationNanos: uint64(2 * time.Second), MeasuredRequests: 1}
			root.Final = true
			u.status, u.model = "Ready", "gpt-6-sol"
			rows, _ := u.mainFrame(width, 10, 0)
			assertNativeUISnapshot(t, fmt.Sprintf("output-throughput-composer-%d", width), rows)
			u.agents.apply(activityPaneEvent{Kind: "agents", Agents: []activityPaneAgent{*root,
				{Name: "/root/fast", OutputThroughput: capturer.OutputThroughput{OutputTokens: 120, DurationNanos: uint64(time.Second), MeasuredRequests: 1}, ContextKnown: true, ContextTokens: 1000, ContextWindow: 200000},
				{Name: "/root/zero", OutputThroughput: capturer.OutputThroughput{DurationNanos: uint64(time.Second), MeasuredRequests: 1}},
				{Name: "/root/absent"},
			}})
			assertNativeUISnapshot(t, fmt.Sprintf("output-throughput-roster-%d", width), u.agents.nativeRoster(width, 6, now, true))
		})
	}
}

func TestOutputThroughputMissingAndPartialEvidence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		time.Sleep(2 * time.Second)
		for _, tc := range []struct {
			name   string
			counts tokenCounts
			time   time.Time
			known  bool
			rate   float64
		}{
			{name: "missing-timing", counts: tokenCounts{TotalsKnown: true, OutputTokens: 80}},
			{name: "missing-totals", counts: tokenCounts{Incomplete: true}, time: started},
			{name: "inconsistent", counts: tokenCounts{TotalsKnown: true, OutputTokens: 80, Inconsistent: true}, time: started},
			{name: "partial-categories-known-totals", counts: tokenCounts{TotalsKnown: true, OutputTokens: 80, Incomplete: true}, time: started, known: true, rate: 40},
		} {
			got := measureOutputThroughput(tc.counts, tc.time, time.Now())
			rate, known := got.Rate()
			if known != tc.known || rate != tc.rate {
				t.Fatalf("%s rate=%g,%v want %g,%v", tc.name, rate, known, tc.rate, tc.known)
			}
		}
		got := measureOutputThroughput(tokenCounts{TotalsKnown: true, OutputTokens: 80}, time.Now(), time.Now())
		if _, known := got.Rate(); known {
			t.Fatal("zero elapsed time was a measured rate")
		}
	})
}

func TestUISnapshotOutputThroughputRestoredRoster(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := usageStoreFixture(t, t.TempDir())
		usage := storedUsageFixture(store)
		measured := usage.observation("child", "child", "gpt-6-sol", "")
		measured.begin()
		time.Sleep(2 * time.Second)
		measured.observe(tokenCounts{TotalsKnown: true, OutputTokens: 100})
		interrupted := usage.observation("interrupted", "interrupted", "gpt-6-sol", "")
		interrupted.begin()
		usage.close()
		u := newAppServerSessionTestUI(t, t.TempDir())
		u.proxy = &mekugiProxy{usage: storedUsageFixture(store)}
		t.Cleanup(u.proxy.usage.close)
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
