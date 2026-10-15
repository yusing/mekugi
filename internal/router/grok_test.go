package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/yusing/mekugi/internal/router/toolplugin"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/responses"
)

func grokTestRequest(t *testing.T, stream bool) []byte {
	t.Helper()
	return mustTestJSON(t, map[string]any{"model": grokModel, "stream": stream, "instructions": "test instructions", "reasoning": map[string]string{"effort": "low"}, "tools": []any{map[string]any{"type": "custom", "name": "exec", "description": "Run exact input"}}, "input": []any{map[string]any{"type": "agent_message", "author": "/root", "recipient": "/root/probe", "content": []any{map[string]string{"type": "input_text", "text": "Run the probe"}}}}})
}
func grokTestHeaders() http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer codex-secret")
	headers.Set(chatGPTAccountIDHeader, "codex-account")
	headers.Set(openAISubagentHeader, "collab_spawn")
	headers.Set(threadIDHeader, "child-thread")
	headers.Set(codexTurnMetadataHeader, `{"subagent_kind":"thread_spawn","request_kind":"regular"}`)
	return headers
}
func grokTestSSE(chunks ...any) string {
	var out strings.Builder
	for _, chunk := range chunks {
		fmt.Fprintf(&out, "data: %s\n\n", responses.MustMarshalJSON(chunk))
	}
	out.WriteString("data: [DONE]\n\n")
	return out.String()
}
func grokTextStream() string {
	return grokTestSSE(map[string]any{"model": "grok-4.6-build", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": "GROK_OK"}, "finish_reason": "stop"}}}, map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4, "prompt_tokens_details": map[string]int{"cached_tokens": 3}, "completion_tokens_details": map[string]int{"reasoning_tokens": 2}}})
}

// Exercise the adapter through the same response transform used by Codex.
func TestGrokToolStreamThroughMekugi(t *testing.T) {
	for _, name := range []string{"exec", "shell", "lookup"} {
		t.Run(name, func(t *testing.T) {
			transform, _, _, _ := newMekugiTestTransform(t)
			kind := "custom"
			arguments := `{"input":"text(1)"}`
			if name == "shell" {
				arguments = `{"input":"pwd"}`
			}
			if name == "lookup" {
				kind = "function"
				arguments = `{"query":"test"}`
			}
			tr := testProviderRequest(t, grokTranslationPolicy(), "", "chat", []any{map[string]string{"type": kind, "name": name}})
			stream := grokTestSSE(map[string]any{"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
					"index": 0, "id": "call-test", "function": map[string]string{"name": name, "arguments": arguments},
				}}}, "finish_reason": "tool_calls",
			}}})
			var completed int
			_, err := tr.codec.Read(strings.NewReader(stream), func(event map[string]any) error {
				visible, err := transform.TransformSSE(mustTestJSON(t, event))
				if err != nil {
					return fmt.Errorf("%s: %w", event["type"], err)
				}
				for _, payload := range visible {
					var envelope map[string]json.RawMessage
					if err := json.Unmarshal(payload, &envelope); err != nil {
						t.Fatal(err)
					}
					if responses.StringField(envelope, "type") == "response.completed" {
						completed++
					}
				}
				return nil
			}, grokTestFailureDetail)
			if err != nil {
				t.Fatal(err)
			}
			if completed != 1 {
				t.Fatalf("completed responses = %d", completed)
			}
		})
	}
}

func TestGrokClientIsolationStreamingJSONAndCancellation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer xai-test" || r.Header.Get(chatGPTAccountIDHeader) != "" || r.Header.Get(openAISubagentHeader) != "" {
					t.Errorf("credential boundary violated")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, grokTextStream())
			}))
			defer server.Close()
			client := &grokClient{httpClient: grokTestHTTPClient(t, server), auth: newGrokAuth("", "xai-test"), streamIdleTimeout: time.Second}
			response, err := client.forwardExecution(t.Context(), t.Context(), grokTestRequest(t, stream), grokTestHeaders())
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(data, []byte("GROK_OK")) || !bytes.Contains(data, []byte("cached_tokens")) {
				t.Fatalf("body=%s", data)
			}
			headers := grokTestHeaders()
			headers.Del(openAISubagentHeader)
			headers.Del(codexTurnMetadataHeader)
			response, err = client.forwardExecution(t.Context(), t.Context(), grokTestRequest(t, stream), headers)
			if err != nil {
				t.Fatal(err)
			}
			data, err = io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(data, []byte("GROK_OK")) {
				t.Fatalf("root body=%s", data)
			}
			if calls.Load() != 2 {
				t.Fatal("root request was not forwarded")
			}
		})
	}
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	client := &grokClient{httpClient: grokTestHTTPClient(t, server), auth: newGrokAuth("", "xai-test")}
	ctx, cancel := context.WithCancel(t.Context())
	response, err := client.forwardExecution(ctx, ctx, grokTestRequest(t, true), grokTestHeaders())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	response.Body.Close()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not reach provider")
	}
}

type grokTestTransport func(*http.Request) (*http.Response, error)

func (f grokTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func grokWriteTestAuth(t *testing.T, path string, expiry time.Time) {
	t.Helper()
	entry := map[string]any{"auth_mode": "oidc", "oidc_issuer": grokIssuer, "oidc_client_id": grokOAuthClientID, "key": "old-token", "refresh_token": "old-refresh", "expires_at": expiry.Format(time.RFC3339Nano), "unrelated": "keep"}
	if err := os.WriteFile(path, mustTestJSON(t, map[string]any{grokIssuer + "::" + grokOAuthClientID: entry, "other-account": map[string]string{"key": "unrelated"}}), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestGrokOAuthRefreshSerializesAndPreservesStore(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "auth.json")
	now := time.Now().UTC()
	grokWriteTestAuth(t, path, now.Add(-time.Minute))
	var exchanges atomic.Int32
	transport := grokTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Error("inference authorization leaked into OAuth")
		}
		if r.Method == "GET" {
			return serverHTTPResponse(`{"issuer":"https://auth.x.ai","token_endpoint":"https://auth.x.ai/token"}`), nil
		}
		exchanges.Add(1)
		r.ParseForm()
		if r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("client_id") != grokOAuthClientID {
			t.Error("bad refresh form")
		}
		return serverHTTPResponse(`{"access_token":"fresh-token","refresh_token":"rotated-refresh","expires_in":3600}`), nil
	})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			a := newGrokAuth(path, "")
			a.httpClient.Transport = transport
			c, err := a.credentials(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			if c.headers.Get("Authorization") != "Bearer fresh-token" {
				t.Error("stale token")
			}
		})
	}
	wg.Wait()
	if exchanges.Load() != 1 {
		t.Fatalf("refreshes=%d", exchanges.Load())
	}
	a := newGrokAuth(path, "")
	store, entry, err := a.read()
	if err != nil {
		t.Fatal(err)
	}
	if responses.StringField(entry, "refresh_token") != "rotated-refresh" || responses.StringField(entry, "unrelated") != "keep" || store["other-account"] == nil {
		t.Fatal("store fields lost")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe credential permissions")
	}
}

func TestGrokOAuthRejectsUntrustedDiscovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	grokWriteTestAuth(t, path, time.Now().Add(-time.Hour))
	a := newGrokAuth(path, "")
	var calls int
	a.httpClient.Transport = grokTestTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return serverHTTPResponse(`{"issuer":"https://auth.x.ai","token_endpoint":"https://attacker.invalid/token"}`), nil
	})
	if _, err := a.credentials(t.Context()); err == nil {
		t.Fatal("accepted credential exfiltration endpoint")
	}
	if calls != 1 {
		t.Fatal("sent refresh token to untrusted endpoint")
	}
}

func TestGrokUnauthorizedRefreshRetriesOnceWithoutCredentialLeak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	grokWriteTestAuth(t, path, time.Now().Add(time.Hour))
	auth := newGrokAuth(path, "")
	auth.httpClient.Transport = grokTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			return serverHTTPResponse(`{"issuer":"https://auth.x.ai","token_endpoint":"https://auth.x.ai/token"}`), nil
		}
		return serverHTTPResponse(`{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`), nil
	})
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get(chatGPTAccountIDHeader) != "" {
			t.Error("leaked Codex account")
		}
		if calls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, "sensitive upstream body")
			return
		}
		if r.Header.Get("Authorization") != "Bearer new-token" {
			t.Error("did not refresh access token")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, grokTextStream())
	}))
	defer server.Close()
	client := &grokClient{httpClient: grokTestHTTPClient(t, server), auth: auth}
	response, err := client.forwardExecution(t.Context(), t.Context(), grokTestRequest(t, false), grokTestHeaders())
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || !bytes.Contains(data, []byte("GROK_OK")) || calls != 2 {
		t.Fatalf("retry calls=%d err=%v", calls, err)
	}
}

func TestGrokDisabledAndUnknownModelFailBeforeProvider(t *testing.T) {
	provider := newProviderClient("http://127.0.0.1:1", nil)
	_, err := provider.forwardExecution(t.Context(), t.Context(), grokTestRequest(t, true), grokTestHeaders(), "")
	if err == nil || !strings.Contains(err.Error(), "mekugi grok") {
		t.Fatalf("disabled route=%v", err)
	}
	body := bytes.Replace(grokTestRequest(t, true), []byte(grokModel), []byte("grok:unknown"), 1)
	if _, err := translateProviderRequest(body, nil); err == nil {
		t.Fatal("accepted unknown model")
	}
}

func TestGrokModelRoutingAndBuildFastCredentialBoundary(t *testing.T) {
	for _, model := range grokModels {
		t.Run(model, func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != model || r.Header.Get("X-Grok-Model-Override") != "" {
					t.Errorf("API route selected model=%v override=%q", body["model"], r.Header.Get("X-Grok-Model-Override"))
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, grokTextStream())
			}))
			defer server.Close()
			client := &grokClient{httpClient: grokTestHTTPClient(t, server), auth: newGrokAuth("", "xai-test")}
			body := mustTestJSON(t, map[string]any{"model": "grok:" + model, "input": []any{}})
			response, err := client.forwardExecution(t.Context(), t.Context(), body, grokTestHeaders())
			if model == "grok-4.7-build-fast" {
				if err == nil || !strings.Contains(err.Error(), "requires Grok OAuth") || calls != 0 {
					t.Fatalf("Build Fast API route: response=%v err=%v calls=%d", response, err, calls)
				}
				return
			}
			if err != nil || calls != 1 {
				t.Fatalf("model %s: response=%v err=%v calls=%d", model, response, err, calls)
			}
			response.Body.Close()
		})
	}

	path := filepath.Join(t.TempDir(), "auth.json")
	grokWriteTestAuth(t, path, time.Now().Add(time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Grok-Model-Override") != "grok-4.7-build-fast" {
			t.Errorf("OAuth model override = %q", r.Header.Get("X-Grok-Model-Override"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, grokTextStream())
	}))
	defer server.Close()
	client := &grokClient{httpClient: grokTestHTTPClient(t, server), auth: newGrokAuth(path, "")}
	body := mustTestJSON(t, map[string]any{"model": "grok:grok-4.7-build-fast", "input": []any{}})
	response, err := client.forwardExecution(t.Context(), t.Context(), body, grokTestHeaders())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func grokTestHTTPClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	transport := client.Transport
	client.Transport = grokTestTransport(func(request *http.Request) (*http.Response, error) {
		request = request.Clone(request.Context())
		request.URL.Scheme = endpoint.Scheme
		request.URL.Host = endpoint.Host
		request.Host = endpoint.Host
		return transport.RoundTrip(request)
	})
	return client
}

func TestGrokCloseUnblocksUpstreamRead(t *testing.T) {
	upstream, writer := io.Pipe()
	defer writer.Close()
	client := &grokClient{auth: newGrokAuth("", "test"), httpClient: &http.Client{Transport: grokTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: upstream}, nil
	})}}
	response, err := client.forwardExecution(t.Context(), t.Context(), grokTestRequest(t, true), grokTestHeaders())
	if err != nil {
		t.Fatal(err)
	}
	// Drain the initial response.created event so translation reaches upstream.Read.
	buffer := make([]byte, 4096)
	if _, err := response.Body.Read(buffer); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- response.Body.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		upstream.Close()
		<-closed
		t.Fatal("Close waited for an upstream read before closing it")
	}
}

func TestGrokAPIKeyStartupWithoutHome(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "ios" {
		t.Skip("os.UserConfigDir requires HOME on Darwin independently of Grok authentication")
	}
	t.Setenv("HOME", "")
	t.Setenv("XAI_API_KEY", "test")
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := RunSession(ctx, toolplugin.Builtins(), []string{"grok"}, nil, func(Session) { cancel() }, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestGrokOutputBudgetRejectedBeforeInference(t *testing.T) {
	client := &grokClient{auth: newGrokAuth("", "test"), httpClient: &http.Client{Transport: grokTestTransport(func(*http.Request) (*http.Response, error) {
		t.Error("unsupported budget reached provider")
		return nil, fmt.Errorf("unexpected inference")
	})}}
	var request map[string]any
	if err := json.Unmarshal(grokTestRequest(t, true), &request); err != nil {
		t.Fatal(err)
	}
	request["max_output_tokens"] = 1000
	if _, err := client.forwardExecution(t.Context(), t.Context(), mustTestJSON(t, request), grokTestHeaders()); err == nil || !strings.Contains(err.Error(), "max_output_tokens") {
		t.Fatalf("expected explicit budget rejection, got %v", err)
	}
	request["max_output_tokens"] = nil
	tr, err := translateProviderRequest(mustTestJSON(t, request), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tr.body["max_tokens"]; ok {
		t.Fatal("forwarded a Chat token budget")
	}
}

func TestGrokWhitespaceAPIKeyUsesOAuthHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XAI_API_KEY", " \t ")
	err := RunSession(t.Context(), toolplugin.Builtins(), []string{"grok"}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "locate Grok credentials") {
		t.Fatalf("expected default OAuth path lookup, got %v", err)
	}
}

// Other models miss that an unprinted nested result never reaches them.
func TestGrokCodeModeExecNamesPrintedResults(t *testing.T) {
	var request map[string]any
	_ = json.Unmarshal(grokTestRequest(t, true), &request)
	request["tools"] = append(request["tools"].([]any), map[string]any{"type": "custom", "name": "other", "description": "Other input"})
	tr, err := translateProviderRequest(mustTestJSON(t, request), nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptions := make(map[string]string)
	for _, tool := range tr.body["tools"].([]any) {
		fn := tool.(map[string]any)["function"].(map[string]any)
		descriptions[fn["name"].(string)] = fn["description"].(string)
	}
	if got := descriptions["exec"]; got != "Run exact input\n"+embeddedInstruction("grok_exec") {
		t.Fatalf("exec description = %q", got)
	}
	if got := descriptions["other"]; got != "Other input" {
		t.Fatalf("other custom tool description = %q", got)
	}
}
