package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func thirdPartyUnauthenticatedHeaders() http.Header {
	headers := grokTestHeaders()
	headers.Del("Authorization")
	headers.Del(chatGPTAccountIDHeader)
	headers.Set("X-Api-Key", "unrelated-provider-secret")
	return headers
}

func assertThirdPartyHeaderIsolation(t *testing.T, headers http.Header, key string) {
	t.Helper()
	if headers.Get("Authorization") != "Bearer "+key {
		t.Error("provider-issued authorization missing or replaced")
	}
	for _, name := range []string{chatGPTAccountIDHeader, threadIDHeader, openAISubagentHeader, codexTurnMetadataHeader, codexSessionIDHeader, "X-Api-Key"} {
		if headers.Get(name) != "" {
			t.Errorf("unrelated credential or Codex header forwarded: %s", name)
		}
	}
}

func TestThirdPartyGrokExecutionWithoutCodexAuthentication(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "json"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assertThirdPartyHeaderIsolation(t, r.Header, "grok-only")
				var wire map[string]any
				if err := json.UnmarshalRead(r.Body, &wire); err != nil || wire["model"] != "grok-4.6" {
					t.Errorf("wrong Grok wire model: %v, %v", wire["model"], err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, grokTextStream())
			}))
			defer server.Close()
			provider := newProviderClient("http://unused.invalid", nil)
			provider.grok = &grokClient{httpClient: grokTestHTTPClient(t, server), auth: newGrokAuth("", "grok-only")}
			var request map[string]any
			if err := json.Unmarshal(grokTestRequest(t, stream), &request); err != nil {
				t.Fatal(err)
			}
			request["model"] = "grok:grok-4.6"
			response, err := provider.forwardExecution(t.Context(), t.Context(), mustTestJSON(t, request), thirdPartyUnauthenticatedHeaders(), "")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || calls.Load() != 1 || !bytes.Contains(body, []byte("GROK_OK")) {
				t.Fatalf("Grok response calls=%d body=%s error=%v", calls.Load(), body, err)
			}
		})
	}
}

func TestThirdPartyOpenCodeExecutionWithoutCodexAuthentication(t *testing.T) {
	config := OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "go-only"}, Zen: OpenCodeServiceConfig{APIKey: "zen-only"}}
	for _, service := range config.services() {
		t.Run(service.prefix, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assertThirdPartyHeaderIsolation(t, r.Header, service.apiKey)
				if r.Header.Get("x-opencode-session") != openCodeSessionID(service.prefix, "child-thread") {
					t.Error("missing provider-scoped session identity")
				}
				var wire map[string]any
				if err := json.UnmarshalRead(r.Body, &wire); err != nil || wire["model"] != openCodeTestModel(service) {
					t.Errorf("wrong OpenCode wire model: %v, %v", wire["model"], err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, grokTextStream())
			}))
			defer server.Close()
			provider := newProviderClient("http://unused.invalid", nil)
			provider.opencode = map[string]*grokClient{service.prefix: {httpClient: grokTestHTTPClient(t, server), openCode: &service}}
			response, err := provider.forwardExecution(t.Context(), t.Context(), openCodeTestRequest(t, service, false), thirdPartyUnauthenticatedHeaders(), "")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || calls.Load() != 1 || !bytes.Contains(body, []byte("GROK_OK")) {
				t.Fatalf("OpenCode response calls=%d body=%s error=%v", calls.Load(), body, err)
			}
		})
	}
}

func TestThirdPartyConfigurationDoesNotBypassOpenAIAuthentication(t *testing.T) {
	client := &http.Client{Transport: grokTestTransport(func(*http.Request) (*http.Response, error) {
		t.Error("unauthenticated OpenAI request reached a provider")
		return serverHTTPResponse("{}"), nil
	})}
	provider := newProviderClient("http://unused.invalid", client)
	provider.grok = &grokClient{httpClient: client, auth: newGrokAuth("", "grok-only")}
	_, err := provider.forwardExecution(t.Context(), t.Context(), []byte(`{"model":"gpt-5.6-sol","input":[]}`), http.Header{}, "")
	if err == nil || !strings.Contains(err.Error(), "Authorization") {
		t.Fatalf("expected OpenAI authentication rejection, got %v", err)
	}
}

func TestThirdPartyStandaloneStartupAuthentication(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		name := "no_credentials"
		if authenticated {
			name = "grok_api_key"
		}
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
				t.Setenv(key, t.TempDir())
			}
			for _, key := range []string{"XAI_API_KEY", "OPENCODE_API_KEY", "OPENCODE_GO_API_KEY", "OPENCODE_ZEN_API_KEY"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			if authenticated {
				t.Setenv("XAI_API_KEY", "test")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			var ready bool
			err := RunSession(ctx, []string{"third-party"}, nil, func(session Session) {
				ready = true
				if !session.GrokEnabled || !session.ThirdPartyOnly {
					t.Errorf("standalone provider flags: GrokEnabled=%t ThirdPartyOnly=%t", session.GrokEnabled, session.ThirdPartyOnly)
				}
				cancel()
			}, nil)
			if authenticated {
				if err != nil || !ready {
					t.Fatalf("authenticated standalone startup: ready=%t error=%v", ready, err)
				}
			} else if err == nil || ready {
				t.Fatalf("unauthenticated standalone must fail before ready: ready=%t error=%v", ready, err)
			} else if message := strings.ToLower(err.Error()); !strings.Contains(message, "credential") && !strings.Contains(message, "api key") && !strings.Contains(message, "authenticat") {
				t.Fatalf("startup failed for a reason other than provider authentication: %v", err)
			}
		})
	}
}
