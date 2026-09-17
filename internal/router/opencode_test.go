package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
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

func openCodeTestModel(service openCodeService) string {
	const id = "glm-5.3-flash"
	if _, ok := service.model(id); !ok {
		panic("embedded OpenCode test model is unavailable")
	}
	return id
}
func openCodeTestRequest(t *testing.T, service openCodeService, stream bool) []byte {
	t.Helper()
	return mustTestJSON(t, map[string]any{
		"model": service.prefix + ":" + openCodeTestModel(service), "stream": stream,
		"input": []any{map[string]string{"role": "user", "content": "Hello"}},
		"tools": []any{map[string]string{"type": "custom", "name": "exec"}},
	})
}

func TestOpenCodeConfig(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", directory)
	// os.UserConfigDir ignores XDG_CONFIG_HOME on some platforms.
	actual, err := os.UserConfigDir()
	if err != nil || actual != directory {
		t.Skip("platform does not use XDG_CONFIG_HOME")
	}
	t.Setenv("OPENCODE_API_KEY", "")
	if err := os.Unsetenv("OPENCODE_API_KEY"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_GO_API_KEY", "")
	t.Setenv("OPENCODE_ZEN_API_KEY", "")
	config, err := loadOpenCodeConfig()
	if err != nil || config.Enabled() {
		t.Fatalf("missing config: %v", err)
	}
	if err := os.Mkdir(filepath.Join(directory, "mekugi"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "mekugi", "config.toml")
	if err := os.WriteFile(path, []byte("[providers.opencode_go]\napi_key = 'config-go'\n[providers.opencode_zen]\napi_key = 'config-zen'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An explicitly empty environment key disables a configured provider.
	config, err = loadOpenCodeConfig()
	if err != nil || config.Enabled() {
		t.Fatalf("empty override: %v", err)
	}
	if err := os.Unsetenv("OPENCODE_GO_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("OPENCODE_ZEN_API_KEY"); err != nil {
		t.Fatal(err)
	}
	config, err = loadOpenCodeConfig()
	if err != nil || config.Go.APIKey != "config-go" || config.Zen.APIKey != "config-zen" {
		t.Fatalf("config not loaded: %v", err)
	}
	t.Setenv("OPENCODE_GO_API_KEY", " env-go ")
	config, err = loadOpenCodeConfig()
	if err != nil || config.Go.APIKey != "env-go" || config.Zen.APIKey != "config-zen" {
		t.Fatalf("provider isolation/precedence: %v", err)
	}
	t.Setenv("OPENCODE_API_KEY", "shared")
	config, err = loadOpenCodeConfig()
	if err != nil || config.Go.APIKey != "env-go" || config.Zen.APIKey != "shared" {
		t.Fatalf("shared key precedence: %v", err)
	}
	t.Setenv("OPENCODE_ZEN_API_KEY", "")
	config, err = loadOpenCodeConfig()
	if err != nil || config.Go.APIKey != "env-go" || config.Zen.APIKey != "" {
		t.Fatalf("service-specific disable after shared key: %v", err)
	}
	for _, body := range []string{
		"[providers.opencode_go]\napi_key = 'private-secret",
		"[providers.opencode_go]\napi_keey = 'private-secret'\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadOpenCodeConfig(); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatalf("unsanitized config error: %v", err)
		}
	}
}

func TestOpenCodeCatalog(t *testing.T) {
	config := OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "go"}, Zen: OpenCodeServiceConfig{APIKey: "zen"}}
	body := []byte(`{"models":[{"slug":"gpt-5.6-sol","multi_agent_version":"v2","shell_type":"unified_exec","apply_patch_tool_type":"freeform","model_messages":{"instructions_template":"native"},"unknown_future":42}]}`)
	catalog, err := ProviderModelCatalog(body, false, config)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Models []map[string]jsontext.Value `json:"models"`
	}
	if err := json.Unmarshal(catalog, &result); err != nil || len(result.Models) != 1+len(config.services()[0].models())+len(config.services()[1].models()) {
		t.Fatalf("model registration: %v", err)
	}
	var levels []map[string]string
	var foundDescription, foundReasoning bool
	for _, model := range result.Models[1:] {
		if string(model["description"]) != `""` {
			foundDescription = true
		}
		levels = nil
		if err := json.Unmarshal(model["supported_reasoning_levels"], &levels); err != nil {
			t.Fatal(err)
		}
		foundReasoning = foundReasoning || len(levels) != 0
		if string(model["shell_type"]) != `"unified_exec"` || string(model["unknown_future"]) != "42" ||
			string(model["use_responses_lite"]) != "false" || string(model["default_reasoning_level"]) != `null` {
			t.Fatalf("inherited incorrect host/provider metadata: %s", mustMarshalJSON(model))
		}
	}
	if !foundDescription || !foundReasoning {
		t.Fatal("embedded provider metadata was not projected")
	}
	again, err := ProviderModelCatalog(catalog, false, config)
	if err != nil || !bytes.Equal(catalog, again) {
		t.Fatalf("catalog not idempotent: %v", err)
	}
	disabled, err := ProviderModelCatalog(catalog, true, OpenCodeConfig{})
	if err != nil || bytes.Contains(disabled, []byte("opencode-")) {
		t.Fatalf("stale disabled providers retained: %v", err)
	}
}

func TestOpenCodeRoutesAndCredentials(t *testing.T) {
	config := OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "go-only"}, Zen: OpenCodeServiceConfig{APIKey: "zen-only"}}
	for _, service := range config.services() {
		t.Run(service.prefix, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				seen := false
				client := &grokClient{openCode: &service, httpClient: &http.Client{
					Transport: grokTestTransport(func(request *http.Request) (*http.Response, error) {
						seen = true
						if request.URL.String() != service.endpoint || request.Header.Get("Authorization") != "Bearer "+service.apiKey {
							t.Error("wrong endpoint or credential")
						}
						if request.Header.Get("x-opencode-session") != openCodeSessionID(service.prefix, "child-thread") {
							t.Error("stable provider-scoped session identity missing")
						}
						for _, name := range []string{chatGPTAccountIDHeader, threadIDHeader, openAISubagentHeader, codexTurnMetadataHeader} {
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
				provider.opencode = map[string]*grokClient{service.prefix: client}
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
			if _, err := provider.forwardExecution(t.Context(), t.Context(), openCodeTestRequest(t, service, false), grokTestHeaders(), ""); err == nil || !strings.Contains(err.Error(), "not configured") {
				t.Fatalf("disabled route leaked to OpenAI: %v", err)
			}
		})
	}
}

func TestOpenCodeReasoningToolReplay(t *testing.T) {
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	tr, err := translateChatRequest(openCodeTestRequest(t, service, false), &service)
	if err != nil {
		t.Fatal(err)
	}
	stream := grokTestSSE(
		map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"reasoning_content": "Think "}}}},
		map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"reasoning_content": "first", "tool_calls": []any{map[string]any{"index": 0, "id": "call1", "function": map[string]string{"name": "exec", "arguments": `{"input":"exact\nprogram"}`}}}}, "finish_reason": "tool_calls"}}},
	)
	result, err := tr.readGrokStream(strings.NewReader(stream), func(map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	input := result["output"].([]any)
	input = append(input, map[string]string{"type": "custom_tool_call_output", "call_id": "call1", "output": "done"})
	request := map[string]any{
		"model": service.prefix + ":" + openCodeTestModel(service),
		"input": input,
		"tools": []any{map[string]string{"type": "custom", "name": "exec"}},
	}
	// Round-trip through the actual CTP history transform as resumed turns do.
	parsed, err := parseResponsesRequest(mustTestJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	identity := func(s string) string { return s }
	history, err := decodeResponsesInput(parsed.fields["input"])
	if err != nil {
		t.Fatal(err)
	}
	codec, err := newCTP2Codec()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transformCTP2Input(&history, identity, identity, newCTP2VisibleLineEncoder(codec), true); err != nil {
		t.Fatal(err)
	}
	encodedHistory, err := history.encode()
	if err != nil {
		t.Fatal(err)
	}
	parsed.setInput(encodedHistory)
	body, err := parsed.wireBody(parsed.fields)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := translateChatRequest(body, &service)
	if err != nil {
		t.Fatal(err)
	}
	messages := replay.body["messages"].([]map[string]any)
	if len(messages) != 2 || messages[0]["reasoning_content"] != "Think first" || messages[1]["tool_call_id"] != "call1" {
		t.Fatalf("reasoning/tool replay lost: %v", messages)
	}
}

func TestOpenCodeRejectsUnsupportedHistoryAndModels(t *testing.T) {
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	for _, input := range []string{
		`[{"type":"reasoning","encrypted_content":"private"}]`,
		`[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"private"}]}]`,
	} {
		_, err := translateChatRequest([]byte(`{"model":"`+service.prefix+`:`+openCodeTestModel(service)+`","input":`+input+`}`), &service)
		diagnostic, ok := errors.AsType[*requestCompatibilityError](err)
		if !ok || diagnostic.code != "opencode_encrypted_history" || strings.Contains(err.Error(), "private") {
			t.Fatalf("wrong local compatibility error: %v", err)
		}
	}
	for _, model := range []string{"opencode-go:unsupported", "opencode-zen:unsupported", grokModel} {
		if _, err := translateChatRequest([]byte(`{"model":"`+model+`","input":[]}`), &service); err == nil {
			t.Fatalf("accepted unsupported model %q", model)
		}
	}
}

func TestOpenCodeWebSocketPrewarmContinuationAndDisconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	started := make(chan []byte, 1)
	stopped := make(chan struct{})
	provider := newProviderClient("http://unused.invalid", nil)
	provider.opencode = map[string]*grokClient{service.prefix: {openCode: &service, httpClient: &http.Client{
		Transport: grokTestTransport(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			started <- body
			<-request.Context().Done()
			close(stopped)
			return nil, request.Context().Err()
		}),
	}}}
	endpoint := responsesWebSocketHandler(ctx, 5*time.Second, provider, nil, nil, nil, nil)
	defer endpoint.Close()
	server := httptest.NewServer(endpoint)
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, server.URL, &websocket.DialOptions{HTTPHeader: grokTestHeaders()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	model := service.prefix + ":" + openCodeTestModel(service)
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
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	model := ""
	for _, candidate := range service.models() {
		if len(candidate.Efforts) == 0 && candidate.Format == "chat" {
			model = candidate.id
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
		request["model"] = service.prefix + ":" + model
		request["reasoning"] = map[string]string{"effort": effort}
		tr, err := translateChatRequest(mustTestJSON(t, request), &service)
		if err != nil {
			t.Fatalf("inherited effort requires manual clearing: %v", err)
		}
		if _, ok := tr.body["reasoning_effort"]; ok {
			t.Fatal("unadvertised effort sent to provider")
		}
	}
}

func TestOpenCodeMixedReasoningMultiTurnReplay(t *testing.T) {
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	var history []any
	for _, answer := range []string{"first answer", "second answer"} {
		history = append(history, map[string]string{"role": "user", "content": "Question"})
		tr, err := translateChatRequest(mustTestJSON(t, map[string]any{
			"model": service.prefix + ":" + openCodeTestModel(service), "input": history,
		}), &service)
		if err != nil {
			t.Fatal(err)
		}
		result, err := tr.readGrokStream(strings.NewReader(grokTestSSE(
			map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{
				"reasoning_content": "reasoning for " + answer, "content": answer,
			}, "finish_reason": "stop"}}},
		)), func(map[string]any) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		history = append(history, result["output"].([]any)...)
	}
	// JSON persistence and a fresh translation must associate reasoning with
	// each original assistant, not just the latest tool-calling turn.
	saved := mustTestJSON(t, map[string]any{"model": service.prefix + ":" + openCodeTestModel(service), "input": history})
	tr, err := translateChatRequest(saved, &service)
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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog := newOpenCodeCatalog()
	snapshot := storeOpenCodeTestSnapshot(catalog, func(snapshot *openCodeSnapshot) {
		snapshot.Updated = time.Now()
	})
	if err := catalog.save(snapshot); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_API_KEY", "shared-test")
	t.Setenv("OPENCODE_GO_API_KEY", "go-test")
	t.Setenv("OPENCODE_ZEN_API_KEY", "zen-test")
	if directory, _ := os.UserConfigDir(); directory != os.Getenv("XDG_CONFIG_HOME") {
		t.Skip("platform does not use XDG_CONFIG_HOME")
	}
	if err := RunSession(t.Context(), []string{"--mode", "passthrough"}, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "require --mode mekugi") {
		t.Fatalf("passthrough accepted OpenCode: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ready := false
	err := RunSession(ctx, []string{"--model-protocol", "native", "--mentor-handoff=false"}, nil, func(session Session) {
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
	service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}}).services()[0]
	body, err := openCodeSnapshotFiles.ReadFile("opencode_snapshot/opencode-go-models.json")
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
			tr, err := translateChatRequest(mustTestJSON(t, map[string]any{
				"model": "opencode-go:" + item.ID, "input": []any{map[string]string{"role": "user", "content": "task"}},
				"tools": []any{map[string]string{"type": "custom", "name": "exec"}},
			}), &service)
			if service.format(item.ID) == "" {
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
				t.Fatalf("unsupported endpoint format %q", tr.format)
			}
		})
	}
}
