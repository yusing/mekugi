package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

const generatedTitleResponse = `{"id":"title","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Improve session naming"}]}]}`

func awaitSessionTitle(t *testing.T, g *sessionTitleGenerator) sessionTitleUpdate {
	t.Helper()
	select {
	case update := <-g.updates:
		return update
	case <-time.After(5 * time.Second):
		t.Fatal("title request did not finish")
		return sessionTitleUpdate{}
	}
}

func TestSessionTitleGenerationRequest(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			provider := serverProviderFunc(func(_, _ context.Context, body []byte, headers http.Header, cacheKey string) (*http.Response, error) {
				// Use the project's existing parser to inspect actual wire fields.
				parsed, err := parseResponsesRequest(body)
				if err != nil {
					t.Error(err)
					return nil, err
				}
				if parsed.model() != "gpt-6-luna" || parsed.reasoningEffort() != "medium" || !parsed.streamResponse {
					t.Errorf("naming settings: %s", body)
				}
				if strings.Contains(string(body), "tools") || journalQuestionFromInput(parsed.fields["input"], "/root") != "First request" {
					t.Errorf("naming input: %s", body)
				}
				if headers.Get("Authorization") != "Bearer test" || headers.Get(chatGPTAccountIDHeader) != "account" || headers.Get(codexTurnMetadataHeader) != "" || headers.Get(threadIDHeader) != "" || cacheKey != "" {
					t.Error("naming request leaked execution identity or lost authentication")
				}
				if stream {
					return serverHTTPResponse("data: {\"type\":\"response.completed\",\"response\":" + generatedTitleResponse + "}\n\n"), nil
				}
				return serverHTTPResponse(generatedTitleResponse), nil
			})
			g := newSessionTitleGenerator(t.Context(), provider, nil)
			g.register(appServerThreadInfo{ID: "main"})
			headers := http.Header{"Authorization": {"Bearer test"}, chatGPTAccountIDHeader: {"account"}}
			headers.Set(codexTurnMetadataHeader, "private-turn")
			headers.Set(threadIDHeader, "main")
			g.observe("main", "First request", headers, false)
			g.observe("main", "Later request", headers, true)
			update := awaitSessionTitle(t, g)
			if update.err != nil || update.name != "Improve session naming" || update.thread != "main" {
				t.Fatalf("update = %+v", update)
			}
		})
	}
}

func TestSessionTitleGenerationEligibility(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		g := newSessionTitleGenerator(t.Context(), serverProviderFunc(func(_, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
			calls.Add(1)
			return serverHTTPResponse(generatedTitleResponse), nil
		}), nil)
		g.register(appServerThreadInfo{ID: "saved", Name: "Saved title"})
		g.register(appServerThreadInfo{ID: "resumed", Turns: []appServerHistoryTurn{{ID: "old"}}})
		g.register(appServerThreadInfo{ID: "fork", Name: "Inherited title"})
		for _, thread := range []string{"saved", "resumed", "fork", "child", "side"} {
			g.observe(thread, "Question", nil, true)
		}
		synctest.Wait()
		g.register(appServerThreadInfo{ID: "fresh"})
		g.observe("fresh", "Question", nil, false)
		if calls.Load() != 0 {
			t.Fatal("generation preceded successful upstream completion")
		}
		g.observe("fresh", "Question", nil, true)
		awaitSessionTitle(t, g)
		g.register(appServerThreadInfo{ID: "fresh"})
		for range 3 {
			g.observe("fresh", "Follow-up", nil, true)
		}
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("naming calls = %d", calls.Load())
		}
	})
}

func TestSessionTitleGenerationFailures(t *testing.T) {
	for _, body := range []string{
		`{"status":"failed","output":[]}`,
		`{"status":"incomplete","output":[]}`,
		`{"status":"completed","output":[]}`,
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Partial\"}\n\n",
	} {
		t.Run(body, func(t *testing.T) {
			g := newSessionTitleGenerator(t.Context(), serverProviderFunc(func(_, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
				return serverHTTPResponse(body), nil
			}), nil)
			if _, err := g.generate(t.Context(), "main", "Question", nil); err == nil {
				t.Fatal("unsuccessful naming response accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	g := newSessionTitleGenerator(ctx, serverProviderFunc(func(_, responseCtx context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
		close(started)
		<-responseCtx.Done()
		return nil, responseCtx.Err()
	}), nil)
	g.register(appServerThreadInfo{ID: "main"})
	g.observe("main", "Question", nil, true)
	<-started
	cancel()
	select {
	case update := <-g.updates:
		if !errors.Is(update.err, context.Canceled) {
			t.Fatal(update.err)
		}
	case <-ctx.Done():
	}
}

func TestSessionTitleUpstreamCompletionTrigger(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, status := range []string{"completed", "failed", "incomplete"} {
			t.Run(fmt.Sprintf("%t/%s", stream, status), func(t *testing.T) {
				var calls atomic.Int32
				g := newSessionTitleGenerator(t.Context(), serverProviderFunc(func(_, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
					calls.Add(1)
					return serverHTTPResponse(generatedTitleResponse), nil
				}), nil)
				g.register(appServerThreadInfo{ID: "main"})
				request := modelTestRequest(t, "gpt-6-sol")
				request.fields["stream"] = mustTestJSON(t, stream)
				request.streamResponse = stream
				headers := serverMetadataHeaders(t, "turn", nil)
				headers.Set(threadIDHeader, "main")
				response := `{"id":"response","status":"` + status + `","output":[]}`
				if stream {
					response = "data: {\"type\":\"response." + status + "\",\"response\":" + response + "}\n\n"
				}
				provider := serverProviderFunc(func(_, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
					return serverHTTPResponse(response), nil
				})
				executor := requestExecutor{provider: provider, output: io.Discard, titleGenerator: g}
				_ = executor.execute(t.Context(), t.Context(), request, headers, "main")
				if status == "completed" {
					if update := awaitSessionTitle(t, g); update.err != nil {
						t.Fatal(update.err)
					}
				} else {
					g.mu.Lock()
					started := g.jobs["main"].started
					g.mu.Unlock()
					if started || calls.Load() != 0 {
						t.Fatal("failed upstream request triggered title generation")
					}
				}
			})
		}
	}
}

func TestSessionTitleNewHostMetadataStopsNaming(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(fmt.Sprint(resumed), func(t *testing.T) {
			g := newSessionTitleGenerator(t.Context(), nil, nil)
			g.register(appServerThreadInfo{ID: "main"})
			if resumed {
				g.register(appServerThreadInfo{ID: "main", Name: "Saved elsewhere"})
			} else {
				name := "Manual name"
				g.named("main", &name)
			}
			if g.needsPrompt("main") {
				t.Fatal("host-named thread remained eligible for automatic naming")
			}
		})
	}
}

func TestSessionTitleHTTPConsumer(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		request, err := parseResponsesRequest(body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if request.model() == sessionTitleModel {
			if request.reasoningEffort() != "medium" {
				t.Error("naming effort was overridden")
			}
			_, _ = io.WriteString(w, generatedTitleResponse)
		} else {
			_, _ = io.WriteString(w, `{"id":"primary","status":"completed","output":[]}`)
		}
	}))
	defer upstream.Close()
	provider := newProviderClient(upstream.URL, upstream.Client())
	cache := newSessionTitleCacheAt("")
	g := newSessionTitleGenerator(t.Context(), provider, cache)
	provider.titleGenerator = g
	g.register(appServerThreadInfo{ID: "main"})
	handler := responsesHandler(t.Context(), defaultRequestTimeout, provider, nil, nil)
	request := modelTestRequest(t, "gpt-6-sol")
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(mustTestJSON(t, request.fields))))
	req.Header = serverMetadataHeaders(t, "turn", nil)
	req.Header.Set(threadIDHeader, "main")
	req.Header.Set("Authorization", "Bearer fixture-token")
	req.Header.Set(chatGPTAccountIDHeader, "fixture-account")
	output := httptest.NewRecorder()
	handler(output, req)
	if output.Code != 200 || output.Body.String() != `{"id":"primary","status":"completed","output":[]}` {
		t.Fatalf("primary response changed: %d %s", output.Code, output.Body)
	}
	update := awaitSessionTitle(t, g)
	if update.err != nil || update.name != "Improve session naming" || calls.Load() != 2 {
		t.Fatalf("title=%+v calls=%d", update, calls.Load())
	}
	if cache.title("main") != "" {
		t.Fatal("generation updated title without host confirmation")
	}
}

func TestSessionTitleUsageAccounting(t *testing.T) {
	for _, transportFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(transportFailure), func(t *testing.T) {
			g := newSessionTitleGenerator(t.Context(), serverProviderFunc(func(_, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
				if transportFailure {
					return nil, errors.New("connection ended after admission")
				}
				response := strings.TrimSuffix(generatedTitleResponse, "}") + `,"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":0},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":0}}}`
				return serverHTTPResponse(response), nil
			}), nil)
			g.usage = newThreadUsage()
			_, _ = g.generate(t.Context(), "main", "Question", nil)
			report, ok := g.usage.snapshot("main")
			if !ok || !strings.Contains(report.model, "gpt-6-luna medium") {
				t.Fatalf("missing naming accounting: %+v", report)
			}
			if transportFailure {
				if report.missingUsage != 1 {
					t.Fatal("transport failure incorrectly claimed complete usage")
				}
			} else if report.InputTokens != 100 || report.OutputTokens != 20 || report.missingUsage != 0 {
				t.Fatalf("title usage = %+v", report)
			}
		})
	}
}
