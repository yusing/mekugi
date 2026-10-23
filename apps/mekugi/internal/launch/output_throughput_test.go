package launch

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

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/charmbracelet/x/ansi"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/yusing/mekugi/backends/codex/binding"
	usagepresentation "github.com/yusing/mekugi/modules/usage/presentation"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/libraries/diffrender"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
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
		setTestAppServerIntegration(u, &observationFixture{usage: restored})
		child := sessionpresentation.ActivityAgent{Name: "/root/child"}
		observeCost(u.usage, "child", &child)
		if usagepresentation.OutputThroughputLabel(child.OutputThroughput) != "50.0 tok/s" {
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
	for _, mode := range []string{"no-proxy", "thread", "module-absent", "diagnostics-absent"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/%s", stream, mode), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var proxy *observationFixture
					if mode != "no-proxy" {
						proxy = newManagedMekugiProxy(t)
						if mode == "module-absent" {
							proxy.usage = nil
						}
					}
					headers := serverMetadataHeaders(t, "turn", nil)
					headers.Set(codexresponses.RoutingSessionHeader, "transport-session")
					req := serverRequest(t, func(f map[string]any) { f["stream"] = stream; f["model"] = "gpt-6-sol" })
					terminal := `{"id":"tps-response","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":80,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":20}}}`
					wire := terminal
					if stream {
						wire = "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"custom_tool_call\",\"id\":\"tool\",\"call_id\":\"tool\",\"name\":\"unknown\",\"input\":\"" + strings.Repeat("inspect carefully ", 64) + "\"}}\n\n" +
							"data: {\"type\":\"response.output_text.delta\",\"output_index\":1,\"delta\":\"additional output\"}\n\n" +
							"data: {\"type\":\"response.completed\",\"response\":" + terminal + "}\n\n"
					}
					var recorder *transportcapture.Recorder
					if mode != "diagnostics-absent" {
						var err error
						recorder, err = new(transportcapture.Service).Open(transportcapture.Config{Mode: "mekugi"})
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = recorder.Close() })
					}
					client := &http.Client{Transport: serverRoundTripper(func(_ *http.Request) (*http.Response, error) {
						time.Sleep(2 * time.Second)
						response := serverHTTPResponse(wire)
						if stream {
							response.Header.Set("Content-Type", "text/event-stream")
						}
						return response, nil
					})}
					if recorder != nil {
						client.Transport = recorder.Transport(client.Transport)
					}
					provider := serverProviderFunc(func(ctx, _ context.Context, body []byte, _ http.Header, _ string) (*http.Response, error) {
						if mode == "diagnostics-absent" && transportcapture.NewStream(ctx) != nil {
							t.Fatal("absent diagnostics created a stream tracker")
						}
						request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://provider.invalid/responses", bytes.NewReader(body))
						if err != nil {
							return nil, err
						}
						return client.Do(request)
					})
					var handler http.Handler = http.HandlerFunc(testResponsesHandler(t.Context(), launchoptions.DefaultRequestTimeout, provider, nil, proxy))
					if recorder != nil {
						handler = recorder.Handler(handler)
					}
					request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(mustTestJSON(t, req.Fields)))
					request.Header = headers
					out := &throughputSlowWriter{ResponseRecorder: httptest.NewRecorder()}
					handler.ServeHTTP(out, request)
					if out.Code != http.StatusOK {
						t.Fatalf("response=%d %s", out.Code, out.Body.String())
					}
					if recorder != nil {
						measurement := recorder.Snapshot().Usage.OutputThroughput
						if rate, known := measurement.Rate(); !known || rate != 40 {
							t.Fatalf("capture throughput=%+v", measurement)
						}
					}
					if mode == "thread" || mode == "diagnostics-absent" {
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
		u.startSession(u.Thread, "")

		usage := newTestUsage(t)

		setTestAppServerIntegration(u, &observationFixture{usage: usage})
		first := usage.Observation(u.Thread, u.Thread, "gpt-6-sol", "")
		first.Begin()
		time.Sleep(time.Second)
		first.Observe(inference.Usage{TotalsKnown: true, OutputTokens: 30})
		applyObservedActivity(&u.attached, u.shell, &u.Client, &u.streams, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.restoring, u.audience)
		if usagepresentation.OutputThroughputLabel(u.attached.session.Agent("/root").OutputThroughput) != "30.0 tok/s" {
			t.Fatal("main did not refresh")
		}
		next := usage.Observation(u.Thread, u.Thread, "gpt-6-sol", "")
		next.Begin()
		applyObservedActivity(&u.attached, u.shell, &u.Client, &u.streams, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.restoring, u.audience)
		if got := usagepresentation.OutputThroughputLabel(u.attached.session.Agent("/root").OutputThroughput); got != "30.0 tok/s" {
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
				u.Thread = "thread-1"
				setTestAppServerIntegration(u, proxy)
				u.startSession(u.Thread, "")

				var previous inference.OutputThroughput
				if prior {
					observation := proxy.usage.Observation(u.Thread, "", "gpt-6.1-sol", "")
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
				httpRequest.Header.Set(codexresponses.RoutingSessionHeader, "transport-session")
				done := make(chan struct{})
				go func() {
					testResponsesHandler(t.Context(), launchoptions.DefaultRequestTimeout, provider, nil, proxy)(out, httpRequest)
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
					applyObservedActivity(&u.attached, u.shell, &u.Client, &u.streams, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.restoring, u.audience)
					if got := u.attached.session.Agent("/root").OutputThroughput; got != previous {
						t.Fatalf("%s replaced measured rate: %+v, want %+v", kind, got, previous)
					}
					time.Sleep(250 * time.Millisecond)
				}
				root := u.attached.session.Agent("/root")
				u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{*root}})
				rows, _ := u.testMainFrame(80, 10, 0)
				if prior {
					assertNativeUISnapshot(t, "output-throughput-live-composer", rows)
					assertNativeUISnapshot(t, "output-throughput-live-roster", u.testActivity(u.shell.Agents).NativeRoster(80, 4, time.Now(), true))
				} else if strings.Contains(strings.Join(rows, "\n"), "tok/s") {
					t.Fatal("first response displayed TPS before provider usage")
				}
				terminal := `data: {"type":"response.completed","response":{"id":"rate","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":130,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":100}}}}` + "\n\n"
				if _, err := io.WriteString(w, terminal); err != nil {
					t.Fatal(err)
				}
				_ = w.Close()
				<-done
				applyObservedActivity(&u.attached, u.shell, &u.Client, &u.streams, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.restoring, u.audience)
				requireRoundThroughput(t, proxy.usage, u.Thread, 40, true)
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
			u.startSession(u.Thread, "")

			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			root := u.attached.session.Agent("/root")
			root.ContextKnown, root.ContextTokens, root.ContextWindow = true, 24000, 200000
			root.OutputThroughput = inference.OutputThroughput{OutputTokens: 83, DurationNanos: uint64(2 * time.Second), MeasuredRequests: 1}
			u.attached.session.Lifecycle(u.Thread).RestoreFinal(true)
			u.attached.presentation.Status.Text, u.model = "Ready", "gpt-6-sol"
			rows, _ := u.testMainFrame(width, 10, 0)
			assertNativeUISnapshot(t, fmt.Sprintf("output-throughput-composer-%d", width), rows)
			u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{*root,
				{Name: "/root/fast", OutputThroughput: inference.OutputThroughput{OutputTokens: 120, DurationNanos: uint64(time.Second), MeasuredRequests: 1}, ContextKnown: true, ContextTokens: 1000, ContextWindow: 200000},
				{Name: "/root/zero", OutputThroughput: inference.OutputThroughput{DurationNanos: uint64(time.Second), MeasuredRequests: 1}},
				{Name: "/root/absent"},
			}})
			assertNativeUISnapshot(t, fmt.Sprintf("output-throughput-roster-%d", width), u.testActivity(u.shell.Agents).NativeRoster(width, 6, now, true))
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
		setTestAppServerIntegration(u, &observationFixture{usage: storedUsageFixture(t, store)})

		for _, thread := range []string{"child", "interrupted", "legacy"} {
			info := binding.ThreadInfo{ID: thread, AgentNickname: thread, Turns: []binding.HistoryTurn{{ID: "old", Status: "completed"}}}
			registerThread(&u.attached.session, info)
			restoreUsage(u.usage, info)
			observeCost(u.usage, thread, u.attached.session.Agent(u.attached.session.KnownCaller(thread)))
			u.attached.session.Lifecycle(thread).Restore(restoredAgentIntervals(info.Turns), uint64(len(info.Turns)), false)
			u.attached.session.Lifecycle(thread).RestoreFinal(true)
		}
		u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Agents: u.attached.session.Roster()})
		u.shell.Agents.Painter.Theme = diffrender.DarkTheme
		u.shell.Agents.Selected = u.attached.session.KnownCaller("child")
		assertNativeUISnapshot(t, "output-throughput-restored-roster", u.testActivity(u.shell.Agents).NativeRoster(120, 8, time.Now(), true))
	})
}

func TestOutputThroughputPresentationSelectionAndIsolation(t *testing.T) {
	for _, mode := range []string{"all", "without usage", "without activity"} {
		t.Run(mode, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			if mode == "without usage" {
				u.presentationState = new(sessionpresentation.Service)
				u.attached.presentation = u.presentationState.Session(u.attached.session.Scope())
				attachPresentation(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread, &u.review, &u.commits, u.ctx, u.retained, u.changeSource, u.activity != nil)
			}
			if mode == "without activity" {
				u.activity = nil
			}

			u.model = "main-model"
			rate := inference.OutputThroughput{OutputTokens: 40, DurationNanos: uint64(time.Second), MeasuredRequests: 1}
			u.attached.session.Agent("/root").OutputThroughput = rate
			before := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
			frame, _ := u.testMainFrame(120, 12, 0)
			if text := ansi.Strip(strings.Join(frame, "\n")); !strings.Contains(text, "main-model") || strings.Contains(text, "40.0 tok/s") != (mode != "without usage") {
				t.Fatalf("Main contribution: %s", text)
			}
			registerThread(&u.attached.session, binding.ThreadInfo{ID: "child", AgentNickname: "child"})
			child := u.attached.session.Agent("/root/child")
			rate.OutputTokens = 80
			child.OutputThroughput = rate
			u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Agents: u.attached.session.Roster()})
			roster := ansi.Strip(strings.Join(u.testActivity(u.shell.Agents).NativeRoster(140, 8, presentationTime(u.clock), true), "\n"))
			if strings.Contains(roster, "80.0 tok/s") != (mode != "without usage") {
				t.Fatalf("roster contribution: %s", roster)
			}
			u.testActivity(u.shell.Agents).displayThread("/root/child")
			frame, _ = u.testMainFrame(120, 12, 0)
			if text := ansi.Strip(strings.Join(frame, "\n")); strings.Contains(text, "40.0 tok/s") || strings.Contains(text, "80.0 tok/s") != (mode != "without usage") {
				t.Fatalf("child contribution: %s", text)
			}
			if mode != "without usage" && (len(before.Caption.Items) != 1 || before.Caption.Items[0].Thread != "main" || before.Caption.Items[0].Text != "40.0 tok/s") {
				t.Fatalf("earlier publication changed: %+v", before.Caption.Items)
			}
		})
	}
}

func TestOutputThroughputPresentationReplayReset(t *testing.T) {
	p := newTestUIReplayPlayback(nil, t.Context(), replayPlaybackTestSource(), 1)
	defer p.close()
	if err := p.seek(p.Until); err != nil {
		t.Fatal(err)
	}
	p.ui.session.Agent("/root").OutputThroughput = inference.OutputThroughput{OutputTokens: 40, DurationNanos: uint64(time.Second), MeasuredRequests: 1}
	p.publish()
	before := p.ui.presentation.View()
	if len(before.Caption.Items) != 1 {
		t.Fatal("admitted replay fact was not published")
	}
	if err := p.seek(0); err != nil {
		t.Fatal(err)
	}
	p.publish()
	after := p.ui.presentation.View()
	if len(after.Caption.Items) != 0 || before.Version.Attachment == after.Version.Attachment {
		t.Fatalf("backward replay retained future facts: %+v", after)
	}
}
