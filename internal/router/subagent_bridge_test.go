package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func bridgeTestRequest(t *testing.T, additional bool) parsedResponsesRequest {
	t.Helper()
	ns := map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{map[string]any{"type": "function", "name": "spawn_agent", "parameters": map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string", "encrypted": true}, "model": map[string]string{"type": "string"}}}}}}
	request := map[string]any{"model": "gpt-test", "instructions": "keep", "tools": []any{ns}, "input": []any{map[string]string{"type": "function_call", "namespace": "collaboration", "name": "spawn_agent", "arguments": "{}", "call_id": "c1"}}}
	if additional {
		request["tools"] = []any{}
		request["input"] = append(request["input"].([]any), map[string]any{"type": "additional_tools", "tools": []any{ns}})
	}
	parsed, err := parseResponsesRequest(mustTestJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
func TestSubagentBridgeProjectsAndRestoresPlaintext(t *testing.T) {
	for _, additional := range []bool{false, true} {
		request := bridgeTestRequest(t, additional)
		bridge, err := prepareSubagentBridge(&request, true)
		if err != nil {
			t.Fatal(err)
		}
		data := mustMarshalJSON(request.fields)
		if bytes.Contains(data, []byte(`"encrypted":true`)) || !bytes.Contains(data, []byte(`"namespace":"mekugi_collaboration"`)) {
			t.Fatalf("projection=%s", data)
		}
		if !strings.Contains(jsonString(request.fields, "instructions"), "plaintext") {
			t.Fatal("missing bridge guidance")
		}
		item := map[string]any{"type": "function_call", "namespace": subagentBridgeNamespace, "name": "spawn_agent", "call_id": "c1", "arguments": `{"message":"plain"}`}
		response, err := bridge.TransformJSON(mustTestJSON(t, map[string]any{"output": []any{item}}))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(response, []byte(`"namespace":"collaboration"`)) || !bytes.Contains(response, []byte(`"encrypted_function_args":[]`)) {
			t.Fatalf("restoration=%s", response)
		}
		events, err := bridge.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": item}))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(events[0], []byte(`"encrypted_function_args":[]`)) {
			t.Fatal("stream marker missing")
		}
		terminal, err := bridge.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{item}}}))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(terminal[0], []byte(`"namespace":"collaboration"`)) {
			t.Fatal("terminal call not restored")
		}
	}
}
func TestSubagentBridgeLeavesRecordedDispatchInstructionUnchanged(t *testing.T) {
	const prior = "Note that collaboration tools cannot be called from inside `functions.exec`. Call `spawn_agent`, `send_message`, `followup_task`, `wait_agent`, `interrupt_agent`, and `list_agents` only as direct tool calls using the recipient shown in their tool definitions, such as `to=functions.collaboration.spawn_agent`, since they are intentionally absent from the `functions.exec` `tools.*` namespace. Available tools in `functions.exec` are explicitly described with a `tools` namespace in the developer message."
	for _, additional := range []bool{false, true} {
		request := bridgeTestRequest(t, additional)
		request.fields["instructions"] = mustTestJSON(t, prior)
		var input []any
		if err := json.Unmarshal(request.fields["input"], &input); err != nil {
			t.Fatal(err)
		}
		offset := len(input)
		input = append(input,
			map[string]any{"type": "message", "role": "developer", "content": prior},
			map[string]any{"type": "message", "role": "developer", "content": []any{
				map[string]any{"type": "input_text", "text": "Keep caller policy.\r\n" + prior + "\r\n"},
			}},
			map[string]any{"type": "message", "role": "user", "content": prior},
		)
		request.setInput(mustTestJSON(t, input))
		if _, err := prepareSubagentBridge(&request, false); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(jsonString(request.fields, "instructions"), prior) {
			t.Fatal("top-level recorded dispatch instruction was rewritten")
		}
		var got []map[string]json.RawMessage
		if err := json.Unmarshal(request.fields["input"], &got); err != nil {
			t.Fatal(err)
		}
		if jsonString(got[offset], "content") != prior {
			t.Fatal("developer scalar dispatch instruction was rewritten")
		}
		var parts []map[string]json.RawMessage
		if err := json.Unmarshal(got[offset+1]["content"], &parts); err != nil {
			t.Fatal(err)
		}
		if jsonString(parts[0], "text") != "Keep caller policy.\r\n"+prior+"\r\n" || jsonString(got[offset+2], "content") != prior {
			t.Fatal("recorded dispatch instruction content was rewritten")
		}
	}
}

func TestGrokCatalogPreservesNativeMetadata(t *testing.T) {
	catalog := []byte(`{"models":[{"slug":"gpt-5.6-sol","multi_agent_version":"v2","use_responses_lite":true,"model_messages":{"instructions_template":"native instructions"},"unknown_future_field":42}],"extra":"keep"}`)
	result, err := ProviderModelCatalog(catalog, true, OpenCodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Models []map[string]json.RawMessage `json:"models"`
		Extra  string                       `json:"extra"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Models) != 2 || parsed.Extra != "keep" || jsonString(parsed.Models[0], "slug") != "gpt-5.6-sol" || jsonString(parsed.Models[1], "slug") != grokModel || string(parsed.Models[1]["unknown_future_field"]) != "42" {
		t.Fatalf("catalog=%s", result)
	}
	if jsonString(parsed.Models[1], "visibility") != "list" || string(parsed.Models[1]["supported_in_api"]) != "true" {
		t.Fatal("Grok is not available in the main-agent model picker")
	}
	if string(parsed.Models[1]["use_responses_lite"]) != "false" {
		t.Fatal("inherited OpenAI lite transport")
	}
	repeated, err := ProviderModelCatalog(result, true, OpenCodeConfig{})
	if err != nil || !bytes.Equal(result, repeated) {
		t.Fatalf("catalog changed when pinning a cached Grok entry: %v", err)
	}
}

func TestSubagentBridgePreservesScalarOpenAIInput(t *testing.T) {
	for _, withTools := range []bool{false, true} {
		request := bridgeTestRequest(t, false)
		request.fields["input"] = mustMarshalJSON("hello")
		if !withTools {
			delete(request.fields, "tools")
		}
		_, err := prepareSubagentBridge(&request, true)
		if err != nil {
			t.Fatal(err)
		}
		if string(request.fields["input"]) != `"hello"` {
			t.Fatal("scalar input was rewritten")
		}
	}
}
func TestGrokCatalogRequiresAnActualV2Template(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		models := []any{map[string]any{"slug": "gpt-5.6-sol", "multi_agent_version": "v1", "marker": "wrong"}}
		if fallback {
			models = append(models, map[string]any{"slug": "other", "multi_agent_version": "v2", "marker": "correct"})
		}
		result, err := ProviderModelCatalog(mustTestJSON(t, map[string]any{"models": models}), true, OpenCodeConfig{})
		if !fallback {
			if err == nil {
				t.Fatal("accepted non-v2 catalog")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var catalog struct {
			Models []map[string]json.RawMessage `json:"models"`
		}
		json.Unmarshal(result, &catalog)
		if jsonString(catalog.Models[len(catalog.Models)-1], "marker") != "correct" {
			t.Fatal("selected non-v2 template")
		}
	}
}

func TestModelsHandlerPreservesNativeCatalogWithGrokEnabled(t *testing.T) {
	body := `{"models":[{"slug":"gpt-5.6-sol","multi_agent_version":"v2"}]}`
	client := &http.Client{Transport: serverRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Etag": []string{`"native-etag"`}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	provider := newProviderClient(testProviderBaseURL, client)
	provider.grok = &grokClient{}
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header = codexAuthHeaders()
	response := httptest.NewRecorder()
	modelsHandler(provider, nil)(response, request)
	if response.Code != http.StatusOK || response.Body.String() != body || response.Header().Get("ETag") != `"native-etag"` {
		t.Fatalf("native catalog was rewritten: status=%d body=%s etag=%s", response.Code, response.Body.String(), response.Header().Get("ETag"))
	}
}

func TestGrokCatalogRebuildsCachedMetadata(t *testing.T) {
	body := []byte(`{"models":[{"slug":"grok:grok-4.6","multi_agent_version":"v2","apply_patch_tool_type":null},{"slug":"gpt-5.6-sol","multi_agent_version":"v2","apply_patch_tool_type":"freeform","shell_type":"unified_exec"}]}`)
	result, err := ProviderModelCatalog(body, true, OpenCodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Models []map[string]json.RawMessage
	}
	if err := json.Unmarshal(result, &catalog); err != nil || len(catalog.Models) != 2 {
		t.Fatalf("rebuilt catalog: %v", err)
	}
	grok := catalog.Models[1]
	if jsonString(grok, "slug") != grokModel || jsonString(grok, "apply_patch_tool_type") != "freeform" || jsonString(grok, "shell_type") != "unified_exec" {
		t.Fatalf("cached Grok tool metadata was not rebuilt: %s", result)
	}
}

func TestSubagentBridgeSpawnArgumentGuidancePreservesNativeContract(t *testing.T) {
	for _, additional := range []bool{false, true} {
		t.Run(fmt.Sprint(additional), func(t *testing.T) {
			schema := map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"message", "task_name"},
				"properties": map[string]any{
					"message":          map[string]any{"type": "string", "encrypted": true, "description": "Native message"},
					"model":            map[string]any{"type": "string", "description": "Native model restrictions"},
					"fork_turns":       map[string]any{"type": "string", "default": "all", "description": "Native context rules"},
					"reasoning_effort": map[string]any{"type": "string", "enum": []string{"low", "medium", "high", "xhigh"}, "description": "Native effort restrictions"},
					"agent_type":       map[string]any{"type": "string", "enum": []string{"default", "review"}, "description": "Fixed role model cannot be overridden"},
					"permission":       map[string]any{"type": "string", "description": "Native permission policy"},
				},
			}
			fn := map[string]any{"type": "function", "name": "spawn_agent", "description": "Native lifecycle rules", "strict": true, "parameters": schema}
			ns := map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{fn}}
			request := bridgeTestRequest(t, false)
			request.fields["tools"] = mustMarshalJSON([]any{ns})
			request.fields["input"] = mustMarshalJSON([]any{})
			if additional {
				request.fields["tools"] = mustMarshalJSON([]any{})
				request.fields["input"] = mustMarshalJSON([]any{map[string]any{"type": "additional_tools", "tools": []any{ns}}})
			}
			bridge, err := prepareSubagentBridge(&request, true)
			if err != nil {
				t.Fatal(err)
			}
			catalog := request.fields["tools"]
			if additional {
				var input []map[string]json.RawMessage
				if err := json.Unmarshal(request.fields["input"], &input); err != nil {
					t.Fatal(err)
				}
				catalog = input[0]["tools"]
			}
			var projected []struct {
				Tools []struct {
					Description string         `json:"description"`
					Strict      bool           `json:"strict"`
					Parameters  map[string]any `json:"parameters"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(catalog, &projected); err != nil {
				t.Fatal(err)
			}
			got := projected[0].Tools[0]
			if !got.Strict || !strings.HasPrefix(got.Description, "Native lifecycle rules") {
				t.Fatalf("native function contract changed: %+v", got)
			}
			properties := got.Parameters["properties"].(map[string]any)
			original := schema["properties"].(map[string]any)
			for _, name := range []string{"model", "fork_turns", "reasoning_effort"} {
				property := properties[name].(map[string]any)
				description := property["description"].(string)
				native := original[name].(map[string]any)["description"].(string)
				if !strings.HasPrefix(description, native+"\n") || !strings.Contains(description, "grok:grok-4.6") {
					t.Fatalf("%s guidance = %q", name, description)
				}
				if name == "fork_turns" && (!strings.Contains(description, `"none"`) || !strings.Contains(description, "complete task")) {
					t.Fatalf("missing fresh-context assignment guidance: %q", description)
				}
				property["description"] = native
			}
			properties["message"].(map[string]any)["encrypted"] = true
			if !bytes.Equal(mustMarshalJSON(got.Parameters), mustMarshalJSON(schema)) {
				t.Fatalf("native schema changed: %s", mustMarshalJSON(got.Parameters))
			}
			arguments := `{"agent_type":"review","model":"grok:grok-4.6","fork_turns":"none","reasoning_effort":"high","message":"complete assignment","permission":"native"}`
			item := mustMarshalJSON(map[string]any{"type": "function_call", "namespace": subagentBridgeNamespace, "name": "spawn_agent", "arguments": arguments, "call_id": "spawn"})
			check := func(raw json.RawMessage) {
				t.Helper()
				var restored map[string]json.RawMessage
				if err := json.Unmarshal(raw, &restored); err != nil {
					t.Fatal(err)
				}
				if jsonString(restored, "arguments") != arguments || jsonString(restored, "namespace") != "collaboration" {
					t.Fatalf("restoration changed arguments or namespace: %s", raw)
				}
			}
			result, err := bridge.TransformJSON(mustMarshalJSON(map[string]any{"output": []json.RawMessage{item}}))
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Output []json.RawMessage `json:"output"`
			}
			if err := json.Unmarshal(result, &response); err != nil {
				t.Fatal(err)
			}
			check(response.Output[0])
			events, err := bridge.TransformSSE(mustMarshalJSON(map[string]any{"type": "response.output_item.done", "item": item}))
			if err != nil {
				t.Fatal(err)
			}
			var event map[string]json.RawMessage
			if err := json.Unmarshal(events[0], &event); err != nil {
				t.Fatal(err)
			}
			check(event["item"])
		})
	}
}

func TestSubagentBridgeDoesNotAddAbsentSpawnArguments(t *testing.T) {
	request := bridgeTestRequest(t, false)
	if _, err := prepareSubagentBridge(&request, true); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(request.fields["tools"], []byte(`"fork_turns":`)) ||
		bytes.Contains(request.fields["tools"], []byte(`"reasoning_effort":`)) {
		t.Fatalf("projection added absent arguments: %s", request.fields["tools"])
	}
}

func TestSubagentBridgeWithoutGrok(t *testing.T) {
	for _, additional := range []bool{false, true} {
		request := bridgeTestRequest(t, additional)
		bridge, err := prepareSubagentBridge(&request, false)
		if err != nil || bridge == nil {
			t.Fatalf("prepare ordinary bridge: %v", err)
		}
		wire := mustMarshalJSON(request.fields)
		if !bytes.Contains(wire, []byte(subagentBridgeNamespace)) || bytes.Contains(wire, []byte("grok:")) ||
			bytes.Contains(wire, []byte(`"encrypted":true`)) {
			t.Fatalf("ordinary projection exposes wrong contract: %s", wire)
		}
		if !strings.Contains(jsonString(request.fields, "instructions"), "message arguments are plaintext") {
			t.Fatal("missing plaintext guidance")
		}
	}
}

func TestOrdinaryCollaborationBridgeAtServerBoundary(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("mekugi=%t/stream=%t", enabled, stream), func(t *testing.T) {
				var proxy *mekugiProxy
				if enabled {
					proxy = newManagedMekugiProxy(t)
				}
				base := bridgeTestRequest(t, false)
				request := serverRequest(t, func(fields map[string]any) {
					fields["tools"] = json.RawMessage(base.fields["tools"])
					fields["stream"] = stream
				})
				namespace := "collaboration"
				if enabled {
					namespace = subagentBridgeNamespace
				}
				const arguments = `{"message":"Boundary assignment","fork_turns":"none"}`
				call := map[string]any{"type": "function_call", "namespace": namespace, "name": "spawn_agent",
					"id": "spawn-item", "call_id": "spawn-call", "arguments": arguments, "status": "completed"}
				terminal := mustTestJSON(t, map[string]any{"id": "boundary", "status": "completed", "output": []any{call}})
				response := serverHTTPResponse(string(terminal))
				if stream {
					response = serverHTTPResponse(finalAnswerTestWire([][]byte{
						mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": call}),
						mustTestJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(terminal)}),
					}))
					response.Header.Set("Content-Type", "text/event-stream")
				}
				provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
				headers := serverMetadataHeaders(t, "turn", map[string]json.RawMessage{t.TempDir(): nil})
				var output bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), request, headers, "bridge-session",
					provider, &output, NewCriticalErrors(), proxy, nil, nil); err != nil {
					t.Fatal(err)
				}
				if len(provider.forwarded) != 1 {
					t.Fatalf("unexpected provider continuations: %d", len(provider.forwarded))
				}
				forwarded := provider.forwarded[0]
				if bytes.Contains(forwarded, []byte(subagentBridgeNamespace)) != enabled ||
					bytes.Contains(forwarded, []byte("grok:")) {
					t.Fatalf("incorrect provider catalog: %s", forwarded)
				}
				if !bytes.Contains(output.Bytes(), []byte(`"namespace":"collaboration"`)) ||
					bytes.Contains(output.Bytes(), []byte(subagentBridgeNamespace)) ||
					bytes.Contains(output.Bytes(), []byte(`"encrypted_function_args":[]`)) != enabled ||
					!bytes.Contains(output.Bytes(), mustTestJSON(t, arguments)) {
					t.Fatalf("incorrect native output: %s", output.Bytes())
				}
			})
		}
	}
}

func TestSubagentBridgePreservesEncryptedHistory(t *testing.T) {
	request := bridgeTestRequest(t, false)
	encrypted := map[string]any{"type": "function_call", "namespace": "collaboration", "name": "spawn_agent",
		"arguments": `{"message":"opaque"}`, "call_id": "old", "encrypted_function_args": []string{"message"}}
	plain := map[string]any{"type": "function_call", "namespace": "collaboration", "name": "spawn_agent",
		"arguments": `{"message":"plaintext"}`, "call_id": "new", "encrypted_function_args": []string{}}
	message := journalTestAssignment("/root/child", "NEW_TASK", "")
	message["content"] = append(message["content"].([]any), map[string]any{"type": "encrypted_content", "encrypted_content": "opaque"})
	request.fields["input"] = mustTestJSON(t, []any{encrypted, message, plain})
	if _, err := prepareSubagentBridge(&request, false); err != nil {
		t.Fatal(err)
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &input); err != nil {
		t.Fatal(err)
	}
	if !sameJSONValue(mustMarshalJSON(input[0]), mustMarshalJSON(encrypted)) ||
		!sameJSONValue(mustMarshalJSON(input[1]), mustMarshalJSON(message)) {
		t.Fatal("old encrypted call or assignment changed")
	}
	if jsonString(input[2], "namespace") != subagentBridgeNamespace ||
		jsonString(input[2], "call_id") != "new" ||
		jsonString(input[2], "arguments") != plain["arguments"] ||
		len(input[2]["encrypted_function_args"]) != 0 {
		t.Fatalf("plaintext replay not projected exactly: %s", mustMarshalJSON(input[2]))
	}
}
