package launch

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

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/providers/openai"
)

const generatedTitleResponse = `{"id":"title","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Improve session naming"}]}]}`

func sessionTitleAuthHeaders() http.Header {
	return http.Header{"Authorization": {"Bearer test"}, openai.AccountIDHeader: {"account"}}
}

func awaitSessionTitle(t *testing.T, g *sessionNaming) sessionTitleUpdate {
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
				parsed, err := codexresponses.ParseRequest(body)
				if err != nil {
					t.Error(err)
					return nil, err
				}
				if parsed.Model() != "gpt-6-luna" || parsed.ReasoningEffort() != "medium" || !parsed.Stream {
					t.Errorf("naming settings: %s", body)
				}
				if strings.Contains(string(body), "tools") || binding.SubmittedUserText(parsed.Fields["input"], true) != "First request" {
					t.Errorf("naming input: %s", body)
				}
				if headers.Get("Authorization") != "Bearer test" || headers.Get(openai.AccountIDHeader) != "account" || headers.Get(codexresponses.TurnMetadataHeader) != "" || headers.Get(codexresponses.ThreadIDHeader) != "" || cacheKey != "" {
					t.Error("naming request leaked execution identity or lost authentication")
				}
				if stream {
					return serverHTTPResponse("data: {\"type\":\"response.completed\",\"response\":" + generatedTitleResponse + "}\n\n"), nil
				}
				return serverHTTPResponse(generatedTitleResponse), nil
			})
			g := newTestSessionNaming(t.Context(), provider)
			g.register(binding.ThreadInfo{ID: "main"})
			headers := sessionTitleAuthHeaders()
			headers.Set(codexresponses.TurnMetadataHeader, "private-turn")
			headers.Set(codexresponses.ThreadIDHeader, "main")
			g.ObserveInput("main", responses.MustMarshalJSON("First request"))
			g.ObserveInput("main", responses.MustMarshalJSON("Later request"))
			g.Completed(t.Context(), "main", headers)
			update := awaitSessionTitle(t, g)
			if update.name != "Improve session naming" || update.thread != "main" {
				t.Fatalf("update = %+v", update)
			}
		})
	}
}

func TestSessionTitleGenerationEligibility(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		g := newTestSessionNaming(t.Context(), serverProviderFunc(func(_, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
			calls.Add(1)
			return serverHTTPResponse(generatedTitleResponse), nil
		}))
		g.register(binding.ThreadInfo{ID: "saved", Name: "Saved title"})
		g.register(binding.ThreadInfo{ID: "resumed", Turns: []binding.HistoryTurn{{ID: "old"}}})
		g.register(binding.ThreadInfo{ID: "fork", Name: "Inherited title"})
		for _, thread := range []string{"saved", "resumed", "fork", "child", "side"} {
			g.ObserveInput(thread, responses.MustMarshalJSON("Question"))
			g.Completed(t.Context(), thread, sessionTitleAuthHeaders())
		}
		synctest.Wait()
		g.register(binding.ThreadInfo{ID: "fresh"})
		g.ObserveInput("fresh", responses.MustMarshalJSON("Question"))
		if calls.Load() != 0 {
			t.Fatal("generation preceded successful upstream completion")
		}
		g.ObserveInput("fresh", responses.MustMarshalJSON("Question"))
		g.Completed(t.Context(), "fresh", sessionTitleAuthHeaders())
		awaitSessionTitle(t, g)
		g.register(binding.ThreadInfo{ID: "fresh"})
		for range 3 {
			g.ObserveInput("fresh", responses.MustMarshalJSON("Follow-up"))
			g.Completed(t.Context(), "fresh", sessionTitleAuthHeaders())
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
			g := newTestSessionNaming(t.Context(), serverProviderFunc(func(_, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
				return serverHTTPResponse(body), nil
			}))
			if _, err := runNamingOperation(t.Context(), g, "main", "Question"); err == nil {
				t.Fatal("unsuccessful naming response accepted")
			}
		})
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		started := make(chan struct{})
		g := newTestSessionNaming(ctx, serverProviderFunc(func(_, responseCtx context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
			close(started)
			<-responseCtx.Done()
			return nil, responseCtx.Err()
		}))
		g.register(binding.ThreadInfo{ID: "main"})
		g.ObserveInput("main", responses.MustMarshalJSON("Question"))
		g.Completed(t.Context(), "main", sessionTitleAuthHeaders())
		<-started
		cancel()
		synctest.Wait()
		if len(g.updates) != 0 {
			t.Fatal("canceled naming published a title update")
		}
	})
}

func TestSessionTitleUpstreamCompletionTrigger(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, status := range []string{"completed", "failed", "incomplete"} {
			t.Run(fmt.Sprintf("%t/%s", stream, status), func(t *testing.T) {
				var calls atomic.Int32
				g := newTestSessionNaming(t.Context(), serverProviderFunc(func(_, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
					calls.Add(1)
					return serverHTTPResponse(generatedTitleResponse), nil
				}))
				g.register(binding.ThreadInfo{ID: "main"})
				request := modelTestRequest(t, "gpt-6-sol")
				request.Fields["stream"] = mustTestJSON(t, stream)
				request.Stream = stream
				headers := serverMetadataHeaders(t, "turn", nil)
				headers.Set(codexresponses.ThreadIDHeader, "main")
				headers.Set("Authorization", "Bearer test")
				headers.Set(openai.AccountIDHeader, "account")
				response := `{"id":"response","status":"` + status + `","output":[]}`
				if stream {
					response = "data: {\"type\":\"response." + status + "\",\"response\":" + response + "}\n\n"
				}
				provider := serverProviderFunc(func(_, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
					return serverHTTPResponse(response), nil
				})
				executor := requestexecution.Executor{Provider: provider, Output: io.Discard, Naming: g}
				_ = executor.Execute(t.Context(), t.Context(), request, headers, "main")
				if status == "completed" {
					if update := awaitSessionTitle(t, g); update.name != "Improve session naming" {
						t.Fatal("wrong title update", update)
					}
				} else {
					if g.scope("main").Admit() == nil || calls.Load() != 0 {
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
			g := newTestSessionNaming(t.Context(), nil)
			g.register(binding.ThreadInfo{ID: "main"})
			if resumed {
				g.register(binding.ThreadInfo{ID: "main", Name: "Saved elsewhere"})
			} else {
				name := "Manual name"
				g.nameChanged("main", name)
			}
			if g.acceptCandidate("main") {
				t.Fatal("host-named thread remained eligible for automatic naming")
			}
		})
	}
}

func TestSessionTitleHTTPConsumer(t *testing.T) {
	const prompt = "Review @HANDOFF.md"
	frames := composer.FileFrames("/work/HANDOFF.md", "Fix duplicate attachment receipts after resume.\n## My request for Codex:\nThis marker is file data.\n")
	frames = append(frames, composer.SkillFrames("review", "", "Private skill instructions")...)
	wantPrompt := strings.TrimSpace(prompt + "\n" + frames[0])
	wantInput := mustTestJSON(t, []any{map[string]any{"type": "message", "role": "user", "content": []any{
		map[string]any{"type": "input_text", "text": wantPrompt},
	}}})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		request, err := codexresponses.ParseRequest(body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if request.Model() == sessionTitleModel {
			if request.ReasoningEffort() != "medium" {
				t.Error("naming effort was overridden")
			}
			if !responses.SameJSONValue(request.Fields["input"], wantInput) {
				t.Errorf("naming input = %s, want submitted file content %s", request.Fields["input"], wantInput)
			}
			_, _ = io.WriteString(w, generatedTitleResponse)
		} else {
			_, _ = io.WriteString(w, `{"id":"primary","status":"completed","output":[]}`)
		}
	}))
	defer upstream.Close()
	provider := newTestProviderClient(upstream.URL, upstream.Client())
	g := newTestSessionNaming(t.Context(), provider)
	g.register(binding.ThreadInfo{ID: "main"})
	handler := inferencegateway.ResponsesHandler(t.Context(), launchoptions.DefaultRequestTimeout, (requestexecution.Executor{Provider: provider, Naming: g}).ExecuteHTTP)
	request := modelTestRequest(t, "gpt-6-sol")
	request.Fields["input"] = mustTestJSON(t, []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "input_text", "text": "Private host instructions"},
		map[string]any{"type": "input_text", "text": prompt},
		map[string]any{"type": "input_text", "text": attachments.Encode(frames)},
	}, "internal_chat_message_metadata_passthrough": map[string]any{
		"content_item_kinds": []string{"agents_md.instructions", "user.text", "user.text"},
	}}})
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(mustTestJSON(t, request.Fields))))
	req.Header = serverMetadataHeaders(t, "turn", nil)
	req.Header.Set(codexresponses.ThreadIDHeader, "main")
	req.Header.Set("Authorization", "Bearer fixture-token")
	req.Header.Set(openai.AccountIDHeader, "fixture-account")
	output := httptest.NewRecorder()
	handler(output, req)
	if output.Code != 200 || output.Body.String() != `{"id":"primary","status":"completed","output":[]}` {
		t.Fatalf("primary response changed: %d %s", output.Code, output.Body)
	}
	update := awaitSessionTitle(t, g)
	if update.name != "Improve session naming" || calls.Load() != 2 {
		t.Fatalf("title=%+v calls=%d", update, calls.Load())
	}
}

func TestSessionTitleNativeProviderHTTPConsumer(t *testing.T) {
	for _, bearer := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("bearer=%v/stream=%v", bearer, stream), func(t *testing.T) {
				var namingCalls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					request, err := codexresponses.ParseRequest(body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					wantAuth := ""
					if bearer {
						wantAuth = "Bearer custom-token"
					}
					if r.URL.Path != "/tenant/responses" || r.URL.RawQuery != "base=a%2Bb&native=x%252Fy" || r.Header.Get("Authorization") != wantAuth || r.Header.Get(openai.AccountIDHeader) != "" || r.Header.Get("X-Provider") != "configured" || r.Header.Get("X-Api-Key") != "configured-key" {
						t.Error("selected provider endpoint, authentication or configured headers changed")
					}
					w.Header().Set("Content-Type", "application/json")
					if request.Model() != sessionTitleModel {
						io.WriteString(w, `{"id":"primary","status":"completed","output":[]}`)
						return
					}
					namingCalls.Add(1)
					for _, name := range []string{codexresponses.ThreadIDHeader, codexresponses.TurnMetadataHeader, codexresponses.ClientRequestIDHeader, codexresponses.WindowIDHeader, codexresponses.SessionIDHeader, codexresponses.RoutingSessionHeader, "x-codex-turn-state", "X-Mekugi-Private", "X-Unrelated-Credential"} {
						if r.Header.Get(name) != "" {
							t.Errorf("auxiliary request leaked %s", name)
						}
					}
					if request.ReasoningEffort() != "medium" || binding.SubmittedUserText(request.Fields["input"], true) != "Name this custom session" {
						t.Error("naming policy/input changed")
					}
					response := strings.TrimSuffix(generatedTitleResponse, "}") + `,"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":0},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":0}}}`
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						response = "data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
					}
					io.WriteString(w, response)
				}))
				defer upstream.Close()
				route, err := profileRoute([]string{"-c", `model_provider="custom"`, "-c", fmt.Sprintf(`model_providers.custom={base_url=%q,http_headers={"X-Provider"="configured","Authorization"="ignored","thread-id"="ignored","session-id"="ignored"},env_http_headers={"X-Api-Key"="PROVIDER_KEY"}}`, upstream.URL+"/tenant?base=a%2Bb")}, &launchoptions.Profile{Name: "custom"})
				if err != nil {
					t.Fatal(err)
				}
				provider := newTestProviderClient("http://openai-must-not-be-used.invalid", upstream.Client())
				route.HTTPClient, provider.Native = upstream.Client(), route
				g := newTestSessionNaming(t.Context(), provider)
				g.usage = newTestUsage(t)
				g.register(binding.ThreadInfo{ID: "main"})
				handler := inferencegateway.ResponsesHandler(t.Context(), launchoptions.DefaultRequestTimeout, (requestexecution.Executor{Provider: provider, Naming: g}).ExecuteHTTP)
				request := modelTestRequest(t, "gpt-6-sol")
				request.Fields["input"] = mustTestJSON(t, "Name this custom session")
				req := httptest.NewRequest("POST", "/v1/responses?native=x%252Fy", strings.NewReader(string(mustTestJSON(t, request.Fields))))
				req.Header = serverMetadataHeaders(t, "turn", nil)
				req.Header.Set(codexresponses.ThreadIDHeader, "main")
				if bearer {
					req.Header.Set("Authorization", "Bearer custom-token")
				}
				req.Header.Set("X-Provider", "configured")
				req.Header.Set("X-Api-Key", "configured-key")
				for _, name := range []string{codexresponses.ClientRequestIDHeader, codexresponses.WindowIDHeader, codexresponses.SessionIDHeader, codexresponses.RoutingSessionHeader, "x-codex-turn-state", "X-Mekugi-Private", "X-Unrelated-Credential"} {
					req.Header.Set(name, "private")
				}
				output := httptest.NewRecorder()
				handler(output, req)
				if output.Code != 200 {
					t.Fatalf("primary response: %d %s", output.Code, output.Body)
				}
				if update := awaitSessionTitle(t, g); update.name != "Improve session naming" || update.thread != "main" || namingCalls.Load() != 1 {
					t.Fatalf("title update: %+v calls=%d", update, namingCalls.Load())
				}
				report, ok := g.usage.Snapshot("main")
				if !ok || report.InputTokens != 100 || report.OutputTokens != 20 || report.MissingUsage != 0 || report.Roundtrips != 1 || report.Cost.Known || report.Cost.Total() != 0 {
					t.Fatalf("custom naming must retain usage without OpenAI prices: %+v", report)
				}
			})
		}
	}
}

func TestSessionTitlePromptKeepsRequestLocal(t *testing.T) {
	request := modelTestRequest(t, "gpt-6-sol")
	request.Fields["input"] = mustTestJSON(t, []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "Review @old.go"},
			map[string]any{"type": "input_text", "text": attachments.Encode(composer.FileFrames("/work/old.go", "Old file content"))},
		}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "image"}}},
	})
	raw := string(request.Fields["input"])
	if got := binding.SubmittedUserText(request.Fields["input"], true); got != "" {
		t.Fatalf("media-only request selected old attachment: %q", got)
	}
	if string(request.Fields["input"]) != raw {
		t.Fatal("title extraction changed execution input")
	}
}

func TestSessionTitleUsageAccounting(t *testing.T) {
	for _, transportFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(transportFailure), func(t *testing.T) {
			g := newTestSessionNaming(t.Context(), serverProviderFunc(func(_, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
				if transportFailure {
					return nil, errors.New("connection ended after admission")
				}
				response := strings.TrimSuffix(generatedTitleResponse, "}") + `,"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":0},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":0}}}`
				return serverHTTPResponse(response), nil
			}))
			g.usage = newTestUsage(t)
			_, _ = runNamingOperation(t.Context(), g, "main", "Question")
			report, ok := g.usage.Snapshot("main")
			if !ok || !strings.Contains(report.Model, "gpt-6-luna medium") {
				t.Fatalf("missing naming accounting: %+v", report)
			}
			if transportFailure {
				if report.MissingUsage != 1 {
					t.Fatal("transport failure incorrectly claimed complete usage")
				}
			} else if report.InputTokens != 100 || report.OutputTokens != 20 || report.MissingUsage != 0 || !report.Cost.Known || report.Cost.Total() <= 0 {
				t.Fatalf("title usage = %+v", report)
			}
		})
	}
}

func TestSessionTitleSkipsWithoutCodexAccess(t *testing.T) {
	for _, tc := range []struct {
		name       string
		headers    http.Header
		noProvider bool
	}{
		{"no credentials", nil, false},
		{"no account", http.Header{"Authorization": {"Bearer test"}}, false},
		{"third-party credentials", http.Header{"Authorization": {"Bearer other-provider"}}, false},
		{"invalid credentials", http.Header{"Authorization": {"Basic other-provider"}, openai.AccountIDHeader: {"account"}}, false},
		{"multiple credentials", http.Header{"Authorization": {"Bearer test", "Bearer other"}, openai.AccountIDHeader: {"account"}}, false},
		{"no API", sessionTitleAuthHeaders(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				var provider inferencegateway.Forwarder = serverProviderFunc(func(_, _ context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
					calls.Add(1)
					return serverHTTPResponse(generatedTitleResponse), nil
				})
				if tc.noProvider {
					provider = nil
				}
				g := newTestSessionNaming(t.Context(), provider)
				g.usage = newTestUsage(t)
				g.register(binding.ThreadInfo{ID: "main"})
				g.ObserveInput("main", responses.MustMarshalJSON("First request"))
				g.Completed(t.Context(), "main", tc.headers)
				g.ObserveInput("main", responses.MustMarshalJSON("Later request"))
				g.Completed(t.Context(), "main", sessionTitleAuthHeaders())
				synctest.Wait()
				if calls.Load() != 0 || len(g.updates) != 0 {
					t.Fatal("skipped naming called a provider or emitted a title update")
				}
				if _, observed := g.usage.Snapshot("main"); observed {
					t.Fatal("skipped naming fabricated usage")
				}
			})
		})
	}
}

func TestSessionTitleUnavailableLunaSkipsSilently(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status         int
		body           string
		transportError bool
	}{
		{"API unreachable", 0, "", true},
		{"auth rejected", 401, `{}`, false},
		{"model denied", 403, `{}`, false},
		{"model missing", 404, `{}`, false},
		{"rate limited", 429, `{}`, false},
		{"service unavailable", 503, `{}`, false},
		{"stream model failure", 200, "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"model_not_found\"}}}\n\n", false},
		{"incomplete response", 200, `{"status":"incomplete","output":[]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				g := newTestSessionNaming(t.Context(), serverProviderFunc(func(_, _ context.Context, body []byte, _ http.Header, _ string) (*http.Response, error) {
					calls.Add(1)
					request, _ := codexresponses.ParseRequest(body)
					if request.Model() != sessionTitleModel || request.ReasoningEffort() != "medium" {
						t.Error("naming fell back to another model or effort")
					}
					if tc.transportError {
						return nil, errors.New("API unavailable")
					}
					response := serverHTTPResponse(tc.body)
					response.StatusCode = tc.status
					return response, nil
				}))
				g.register(binding.ThreadInfo{ID: "main"})
				g.ObserveInput("main", responses.MustMarshalJSON("Question"))
				g.Completed(t.Context(), "main", sessionTitleAuthHeaders())
				synctest.Wait()
				g.ObserveInput("main", responses.MustMarshalJSON("Follow-up"))
				g.Completed(t.Context(), "main", sessionTitleAuthHeaders())
				synctest.Wait()
				if calls.Load() != 1 || len(g.updates) != 0 {
					t.Fatal("unavailable Luna emitted an update, retried, or fell back")
				}
			})
		})
	}
}

func TestSessionTitleFailedManualRenameSurvivesResume(t *testing.T) {
	u, wire := newAppServerTestUI()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	g := newTestSessionNaming(ctx, serverProviderFunc(func(_, responseCtx context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
		close(started)
		select {
		case <-release:
			return serverHTTPResponse(generatedTitleResponse), nil
		case <-responseCtx.Done():
			return nil, responseCtx.Err()
		}
	}))
	u.titleGenerator = g
	g.register(binding.ThreadInfo{ID: "main"})
	g.ObserveInput("main", responses.MustMarshalJSON("First request"))
	g.Completed(t.Context(), "main", sessionTitleAuthHeaders())
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("naming inference did not start")
	}
	appServerTestKeys(t, u, "/title Manual intent\r")
	save := sessionTitleTestOne(t, wire)
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"save failed"}}`, save.ID))
	if u.attached.feedback.Text == "" {
		t.Fatal("manual save failure was not reported")
	}
	// Detach the original view, then resume its still-unnamed completed turn.
	u.Replacement.Clear()
	appServerTestRequest(t, u, "100", "thread/start")
	appServerTestMessage(t, u, `{"id":100,"result":{"thread":{"id":"other"}}}`)
	_ = resumeTestRequests(t, wire)
	u.Replacement.Resume("main")
	appServerTestRequest(t, u, "101", "thread/resume")
	appServerTestMessage(t, u, `{"id":101,"result":{"thread":{"id":"main","turns":[{"id":"first","status":"completed","items":[]}]}}}`)
	_ = resumeTestRequests(t, wire)
	close(release)
	persistSessionTitle(&u.Client, u.titleGenerator, &u.attached.feedback, u.clock, awaitSessionTitle(t, g))
	if wire.Len() != 0 {
		t.Fatalf("failed manual suppression lost on resume: %s", wire.String())
	}
}
