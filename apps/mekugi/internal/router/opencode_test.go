package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/providers/openai"

	"github.com/yusing/mekugi/providers/opencode"

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
)

func openCodeTestModel(service opencode.Service) string {
	const id = "glm-5.3-flash"
	if _, ok := service.Model(id); !ok {
		panic("embedded OpenCode test model is unavailable")
	}
	return id
}
func openCodeTestRequest(t *testing.T, service opencode.Service, stream bool) []byte {
	t.Helper()
	return mustTestJSON(t, map[string]any{
		"model": service.Prefix + ":" + openCodeTestModel(service), "stream": stream,
		"input": []any{map[string]string{"role": "user", "content": "Hello"}},
		"tools": []any{map[string]string{"type": "custom", "name": "exec"}},
	})
}

func TestOpenCodeRoutesAndCredentials(t *testing.T) {
	config := opencode.Config{Go: opencode.ServiceConfig{APIKey: "go-only"}, Zen: opencode.ServiceConfig{APIKey: "zen-only"}}
	for _, service := range config.Services() {
		t.Run(service.Prefix, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				seen := false
				client := &inferencegateway.ThirdPartyClient{Diagnostics: providerCodecDiagnostics(), OpenCode: &service, HTTPClient: &http.Client{
					Transport: grokTestTransport(func(request *http.Request) (*http.Response, error) {
						seen = true
						if request.URL.String() != service.Endpoint || request.Header.Get("Authorization") != "Bearer "+service.APIKey {
							t.Error("wrong endpoint or credential")
						}
						if request.Header.Get("x-opencode-session") != openCodeTestChildSessionIDs[service.Prefix] {
							t.Error("stable provider-scoped session identity missing")
						}
						for _, name := range []string{openai.AccountIDHeader, codexresponses.ThreadIDHeader, codexresponses.SubagentHeader, codexresponses.TurnMetadataHeader} {
							if request.Header.Get(name) != "" {
								t.Errorf("Codex header forwarded: %s", name)
							}
						}
						body, _ := io.ReadAll(request.Body)
						var wire map[string]any
						if err := json.Unmarshal(body, &wire); err != nil || wire["model"] != openCodeTestModel(service) || wire["stream"] != true {
							t.Errorf("wrong wire request: %s", body)
						}
						return serverHTTPResponse(grokTestSSE(
							map[string]any{"model": "provider-alias", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": "OK"}, "finish_reason": "stop"}}},
							map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4, "completion_tokens_details": map[string]int{"reasoning_tokens": 2}}},
						)), nil
					}),
				}}
				provider := newTestProviderClient("http://unused.invalid", nil)
				provider.OpenCode = map[string]*inferencegateway.ThirdPartyClient{service.Prefix: client}
				response, err := provider.ForwardExecution(t.Context(), t.Context(), openCodeTestRequest(t, service, stream), grokTestHeaders(), "")
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || !seen || !bytes.Contains(body, []byte("provider-alias")) ||
					!bytes.Contains(body, []byte(`"output_tokens":4`)) || bytes.Contains(body, []byte(`"output_tokens":6`)) {
					t.Fatalf("wrong response/usage: %s, %v", body, err)
				}
			}
			provider := newTestProviderClient("http://unused.invalid", nil)
			_, err := provider.ForwardExecution(t.Context(), t.Context(), openCodeTestRequest(t, service, false), grokTestHeaders(), "")
			if compatibility, ok := err.(*inferencegateway.CompatibilityError); !ok || compatibility.Code != "opencode_not_configured" {
				t.Fatalf("disabled route was not rejected locally: %v", err)
			}
		})
	}
}

func TestOpenCodeWebSocketPrewarmContinuationAndDisconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	started := make(chan []byte, 1)
	stopped := make(chan struct{})
	provider := newTestProviderClient("http://unused.invalid", nil)
	provider.OpenCode = map[string]*inferencegateway.ThirdPartyClient{service.Prefix: {Diagnostics: providerCodecDiagnostics(), OpenCode: &service, HTTPClient: &http.Client{
		Transport: grokTestTransport(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			started <- body
			<-request.Context().Done()
			close(stopped)
			return nil, request.Context().Err()
		}),
	}}}
	endpoint := testWebSocketEndpoint(ctx, 5*time.Second, provider, nil, nil)
	defer endpoint.Close()
	server := httptest.NewServer(endpoint)
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: grokTestHeaders()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	model := service.Prefix + ":" + openCodeTestModel(service)
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": model, "generate": false, "input": "warm up"})
	event := socketRead(t, ctx, conn)
	var response map[string]jsontext.Value
	if err := json.Unmarshal(event["response"], &response); err != nil || string(event["type"]) != `"response.completed"` {
		t.Fatalf("prewarm: %v", err)
	}
	select {
	case <-started:
		t.Fatal("prewarm generated inference")
	default:
	}
	var parentID string
	_ = json.Unmarshal(response["id"], &parentID)
	socketWrite(t, ctx, conn, map[string]any{"type": "response.create", "model": model, "previous_response_id": parentID, "input": []any{}})
	select {
	case body := <-started:
		if !bytes.Contains(body, []byte("warm up")) || bytes.Contains(body, []byte("previous_response_id")) {
			t.Fatalf("continuation lost full history: %s", body)
		}
	case <-ctx.Done():
		t.Fatal("continuation did not start")
	}
	conn.CloseNow()
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("disconnect did not cancel OpenCode")
	}
}

func TestOpenCodeStartupAndMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	directory, _ := os.UserCacheDir()
	path := filepath.Join(directory, "mekugi", "opencode-models-v2.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	models := map[string]opencode.Metadata{"test": {Format: "chat"}}
	if err := os.WriteFile(path, mustTestJSON(t, map[string]any{"version": 2, "updated": time.Now(), "models": map[string]any{"opencode-go": models, "opencode-zen": models}}), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OPENCODE_API_KEY", "shared-test")
	t.Setenv("OPENCODE_GO_API_KEY", "go-test")
	t.Setenv("OPENCODE_ZEN_API_KEY", "zen-test")
	if directory, _ := os.UserConfigDir(); directory != os.Getenv("XDG_CONFIG_HOME") {
		t.Skip("platform does not use XDG_CONFIG_HOME")
	}
	if err := testRunSession(testHistory(), testRecovery(), t.Context(), testCommands(), []string{"--mode", "passthrough", "third-party"}, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "require --mode mekugi") {
		t.Fatalf("passthrough accepted OpenCode: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ready := false
	err := testRunSession(testHistory(), testRecovery(), ctx, testCommands(), []string{"third-party"}, nil, func(session Session) {
		ready = true
		if session.GrokEnabled || session.OpenCode.Go.APIKey != "go-test" || session.OpenCode.Zen.APIKey != "zen-test" {
			t.Error("startup lost separate OpenCode settings")
		}
		cancel()
	}, nil)
	if err != nil || !ready {
		t.Fatalf("OpenCode startup failed: %v", err)
	}
}

// Fixed wire fixtures for the child-thread identity used by routing tests.
var openCodeTestChildSessionIDs = map[string]string{
	"opencode-go":  "mekugi-8e83a0c5d15e6eabd13205400fefbf3a459361acadb52197444c792e71839539",
	"opencode-zen": "mekugi-90b8d63d82801fc35cd7beb0238f3758dfd91fbf10123116d8670bbc929b45c4",
}
