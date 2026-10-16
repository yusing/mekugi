package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/providers/openai"

	"github.com/yusing/mekugi/providers/opencode"

	json "encoding/json/v2"
	"errors"
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
				client := &grokClient{openCode: &service, httpClient: &http.Client{
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
				provider := newProviderClient("http://unused.invalid", nil)
				provider.opencode = map[string]*grokClient{service.Prefix: client}
				response, err := provider.forwardExecution(t.Context(), t.Context(), openCodeTestRequest(t, service, stream), grokTestHeaders(), "")
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
			provider := newProviderClient("http://unused.invalid", nil)
			_, err := provider.forwardExecution(t.Context(), t.Context(), openCodeTestRequest(t, service, false), grokTestHeaders(), "")
			if compatibility, ok := err.(*requestCompatibilityError); !ok || compatibility.code != "opencode_not_configured" {
				t.Fatalf("disabled route was not rejected locally: %v", err)
			}
		})
	}
}

func TestProviderHostedSearchGuidanceMatchesRoute(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	for _, tc := range []struct {
		model   string
		service *opencode.Service
	}{
		{"grok-4.7", nil},
		{service.Prefix + ":" + openCodeTestModel(service), &service},
	} {
		t.Run(tc.model, func(t *testing.T) {
			request := mustTestJSON(t, map[string]any{
				"model": tc.model, "instructions": "Keep caller policy.", "input": []any{},
				"tools": []any{map[string]string{"type": "web_search"}},
			})
			translated, err := translateProviderRequest(request, tc.service)
			if err != nil {
				t.Fatal(err)
			}
			wire := string(mustTestJSON(t, translated.body))
			if !strings.Contains(wire, "Keep caller policy.") || !strings.Contains(wire, "OpenAI-hosted web search is unavailable on this route.") {
				t.Fatalf("missing caller or route-neutral search guidance: %s", wire)
			}
		})
	}
}

func TestOpenCodeRequestDiagnosticIdentity(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	request := mustTestJSON(t, map[string]any{
		"model":                service.Prefix + ":" + openCodeTestModel(service),
		"previous_response_id": "old",
		"input":                []any{},
	})
	_, err := translateProviderRequest(request, &service)
	if err == nil || !strings.Contains(err.Error(), "OpenCode requires explicit conversation history") ||
		strings.Contains(err.Error(), "grok requires") {
		t.Fatalf("OpenCode error lost provider identity: %v", err)
	}
}

func TestOpenCodeReasoningToolReplay(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	tr, err := translateProviderRequest(openCodeTestRequest(t, service, false), &service)
	if err != nil {
		t.Fatal(err)
	}
	stream := grokTestSSE(
		map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"reasoning_content": "Think "}}}},
		map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"reasoning_content": "first", "tool_calls": []any{map[string]any{"index": 0, "id": "call1", "function": map[string]string{"name": "exec", "arguments": `{"input":"exact\nprogram"}`}}}}, "finish_reason": "tool_calls"}}},
	)
	result, err := tr.codec.Read(strings.NewReader(stream), func(map[string]any) error { return nil }, openCodeTestFailureDetail)
	if err != nil {
		t.Fatal(err)
	}
	input := result["output"].([]any)
	input = append(input, map[string]string{"type": "custom_tool_call_output", "call_id": "call1", "output": "done"})
	request := map[string]any{
		"model": service.Prefix + ":" + openCodeTestModel(service),
		"input": input,
		"tools": []any{map[string]string{"type": "custom", "name": "exec"}},
	}
	parsed, err := codexresponses.ParseRequest(mustTestJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	body, err := parsed.WireBody(parsed.Fields)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := translateProviderRequest(body, &service)
	if err != nil {
		t.Fatal(err)
	}
	messages := replay.body["messages"].([]map[string]any)
	if len(messages) != 2 || messages[0]["reasoning_content"] != "Think first" || messages[1]["tool_call_id"] != "call1" {
		t.Fatalf("reasoning/tool replay lost: %v", messages)
	}
}

func TestOpenCodeRejectsUnsupportedHistoryAndModels(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	for _, input := range []string{
		`[{"type":"reasoning","encrypted_content":"private"}]`,
		`[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"private"}]}]`,
	} {
		_, err := translateProviderRequest([]byte(`{"model":"`+service.Prefix+`:`+openCodeTestModel(service)+`","input":`+input+`}`), &service)
		diagnostic, ok := errors.AsType[*requestCompatibilityError](err)
		if !ok || diagnostic.code != "opencode_encrypted_history" || strings.Contains(err.Error(), "private") {
			t.Fatalf("wrong local compatibility error: %v", err)
		}
	}
	for _, model := range []string{"opencode-go:unsupported", "opencode-zen:unsupported", grokModel} {
		if _, err := translateProviderRequest([]byte(`{"model":"`+model+`","input":[]}`), &service); err == nil {
			t.Fatalf("accepted unsupported model %q", model)
		}
	}
}

func TestOpenCodeWebSocketPrewarmContinuationAndDisconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	started := make(chan []byte, 1)
	stopped := make(chan struct{})
	provider := newProviderClient("http://unused.invalid", nil)
	provider.opencode = map[string]*grokClient{service.Prefix: {openCode: &service, httpClient: &http.Client{
		Transport: grokTestTransport(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			started <- body
			<-request.Context().Done()
			close(stopped)
			return nil, request.Context().Err()
		}),
	}}}
	endpoint := responsesWebSocketHandler(ctx, 5*time.Second, provider, nil, nil)
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

func TestOpenCodeInheritedReasoningEffort(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	model := ""
	for _, candidate := range service.Models() {
		if len(candidate.Efforts) == 0 && candidate.Format == "chat" {
			model = candidate.ID
			break
		}
	}
	if model == "" {
		t.Fatal("embedded snapshot has no non-reasoning Chat model")
	}
	for _, effort := range []string{"", "none", "high", "max"} {
		var request map[string]any
		if err := json.Unmarshal(openCodeTestRequest(t, service, false), &request); err != nil {
			t.Fatal(err)
		}
		request["model"] = service.Prefix + ":" + model
		request["reasoning"] = map[string]string{"effort": effort}
		tr, err := translateProviderRequest(mustTestJSON(t, request), &service)
		if err != nil {
			t.Fatalf("inherited effort requires manual clearing: %v", err)
		}
		if _, ok := tr.body["reasoning_effort"]; ok {
			t.Fatal("unadvertised effort sent to provider")
		}
	}
}

func TestOpenCodeMixedReasoningMultiTurnReplay(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	var history []any
	for _, answer := range []string{"first answer", "second answer"} {
		history = append(history, map[string]string{"role": "user", "content": "Question"})
		tr, err := translateProviderRequest(mustTestJSON(t, map[string]any{
			"model": service.Prefix + ":" + openCodeTestModel(service), "input": history,
		}), &service)
		if err != nil {
			t.Fatal(err)
		}
		result, err := tr.codec.Read(strings.NewReader(grokTestSSE(
			map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{
				"reasoning_content": "reasoning for " + answer, "content": answer,
			}, "finish_reason": "stop"}}},
		)), func(map[string]any) error { return nil }, openCodeTestFailureDetail)
		if err != nil {
			t.Fatal(err)
		}
		history = append(history, result["output"].([]any)...)
	}
	// JSON persistence and a fresh translation must associate reasoning with
	// each original assistant, not just the latest tool-calling turn.
	saved := mustTestJSON(t, map[string]any{"model": service.Prefix + ":" + openCodeTestModel(service), "input": history})
	tr, err := translateProviderRequest(saved, &service)
	if err != nil {
		t.Fatal(err)
	}
	messages := tr.body["messages"].([]map[string]any)
	if len(messages) != 4 || messages[1]["reasoning_content"] != "reasoning for first answer" ||
		messages[3]["reasoning_content"] != "reasoning for second answer" {
		t.Fatalf("multi-turn reasoning association lost: %v", messages)
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

func TestEveryEmbeddedGoModelHasAnEndpoint(t *testing.T) {
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}}).Services()[0]
	body, err := os.ReadFile("../../../../providers/opencode/snapshot/opencode-go-models.json")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil || len(list.Data) == 0 {
		t.Fatalf("embedded model list: %v", err)
	}
	for _, item := range list.Data {
		t.Run(item.ID, func(t *testing.T) {
			tr, err := translateProviderRequest(mustTestJSON(t, map[string]any{
				"model": "opencode-go:" + item.ID, "input": []any{map[string]string{"role": "user", "content": "task"}},
				"tools": []any{map[string]string{"type": "custom", "name": "exec"}},
			}), &service)
			if service.Format(item.ID) == "" {
				if err == nil {
					t.Fatal("model without provider format was advertised")
				}
				return
			}
			if err != nil || tr.body["model"] != item.ID {
				t.Fatalf("Go model unavailable: %v", err)
			}
			switch tr.format {
			case "chat":
				if tr.body["messages"] == nil {
					t.Fatal("Chat endpoint body missing messages")
				}
			case "anthropic":
				if tr.body["max_tokens"] == nil {
					t.Fatal("Anthropic endpoint body missing max_tokens")
				}
			case "responses":
				if tr.body["input"] == nil {
					t.Fatal("Responses endpoint body missing input")
				}
			default:
				t.Fatalf("unsupported endpoint adapter %T", tr.format)
			}
		})
	}
}

// Fixed wire fixtures for the child-thread identity used by routing tests.
var openCodeTestChildSessionIDs = map[string]string{
	"opencode-go":  "mekugi-8e83a0c5d15e6eabd13205400fefbf3a459361acadb52197444c792e71839539",
	"opencode-zen": "mekugi-90b8d63d82801fc35cd7beb0238f3758dfd91fbf10123116d8670bbc929b45c4",
}
