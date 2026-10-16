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

	"github.com/yusing/mekugi/providers/grok"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func bridgeTestRequest(t *testing.T, additional bool) codexresponses.Request {
	t.Helper()
	ns := map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{map[string]any{"type": "function", "name": "spawn_agent", "parameters": map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string", "encrypted": true}, "model": map[string]string{"type": "string"}}}}}}
	request := map[string]any{"model": "gpt-test", "instructions": "keep", "tools": []any{ns}, "input": []any{map[string]string{"type": "function_call", "namespace": "collaboration", "name": "spawn_agent", "arguments": "{}", "call_id": "c1"}}}
	if additional {
		request["tools"] = []any{}
		request["input"] = append(request["input"].([]any), map[string]any{"type": "additional_tools", "tools": []any{ns}})
	}
	parsed, err := codexresponses.ParseRequest(mustTestJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
func TestSubagentBridgeProjectsAndRestoresPlaintext(t *testing.T) {
	for _, additional := range []bool{false, true} {
		request := bridgeTestRequest(t, additional)
		bridge, err := prepareSubagentBridge(&request, true, false)
		if err != nil {
			t.Fatal(err)
		}
		data := responses.MustMarshalJSON(request.Fields)
		for _, model := range grok.Models() {
			if !bytes.Contains(data, []byte("grok:"+model)) {
				t.Fatalf("model missing from projected override guidance: %s", model)
			}
		}
		if bytes.Contains(data, []byte("%MODELS%")) {
			t.Fatal("unexpanded Grok model catalog")
		}

		if bytes.Contains(data, []byte(`"encrypted":true`)) || !bytes.Contains(data, []byte(`"namespace":"mekugi_collaboration"`)) {
			t.Fatalf("projection=%s", data)
		}
		if responses.StringField(request.Fields, "instructions") != "keep" || !bytes.Contains(data, responses.MustMarshalJSON(collaborationNamespaceInstruction)) {
			t.Fatal("bridge guidance changed base instructions or is missing from the projected namespace")
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
		request.Fields["instructions"] = mustTestJSON(t, prior)
		var input []any
		if err := json.Unmarshal(request.Fields["input"], &input); err != nil {
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
		request.Fields["input"] = mustTestJSON(t, input)
		if _, err := prepareSubagentBridge(&request, false, false); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(responses.StringField(request.Fields, "instructions"), prior) {
			t.Fatal("top-level recorded dispatch instruction was rewritten")
		}
		var got []map[string]json.RawMessage
		if err := json.Unmarshal(request.Fields["input"], &got); err != nil {
			t.Fatal(err)
		}
		if responses.StringField(got[offset], "content") != prior {
			t.Fatal("developer scalar dispatch instruction was rewritten")
		}
		var parts []map[string]json.RawMessage
		if err := json.Unmarshal(got[offset+1]["content"], &parts); err != nil {
			t.Fatal(err)
		}
		if responses.StringField(parts[0], "text") != "Keep caller policy.\r\n"+prior+"\r\n" || responses.StringField(got[offset+2], "content") != prior {
			t.Fatal("recorded dispatch instruction content was rewritten")
		}
	}
}

func TestSubagentBridgePreservesScalarOpenAIInput(t *testing.T) {
	for _, withTools := range []bool{false, true} {
		request := bridgeTestRequest(t, false)
		request.Fields["input"] = responses.MustMarshalJSON("hello")
		if !withTools {
			delete(request.Fields, "tools")
		}
		_, err := prepareSubagentBridge(&request, true, false)
		if err != nil {
			t.Fatal(err)
		}
		if string(request.Fields["input"]) != `"hello"` {
			t.Fatal("scalar input was rewritten")
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
			request.Fields["tools"] = responses.MustMarshalJSON([]any{ns})
			request.Fields["input"] = responses.MustMarshalJSON([]any{})
			if additional {
				request.Fields["tools"] = responses.MustMarshalJSON([]any{})
				request.Fields["input"] = responses.MustMarshalJSON([]any{map[string]any{"type": "additional_tools", "tools": []any{ns}}})
			}
			bridge, err := prepareSubagentBridge(&request, true, false)
			if err != nil {
				t.Fatal(err)
			}
			catalog := request.Fields["tools"]
			if additional {
				var input []map[string]json.RawMessage
				if err := json.Unmarshal(request.Fields["input"], &input); err != nil {
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
			if !got.Strict || got.Description != "Native lifecycle rules" {
				t.Fatalf("native function contract changed: %+v", got)
			}
			properties := got.Parameters["properties"].(map[string]any)
			original := schema["properties"].(map[string]any)
			for _, name := range []string{"model", "fork_turns", "reasoning_effort"} {
				property := properties[name].(map[string]any)
				description := property["description"].(string)
				native := original[name].(map[string]any)["description"].(string)
				if name != "model" && description != native+"\n"+map[string]string{"fork_turns": grokForkTurnsInstruction, "reasoning_effort": grokReasoningEffortInstruction}[name] {
					t.Fatalf("%s guidance = %q", name, description)
				}
				if name == "model" {
					for _, model := range grok.Models() {
						if strings.Count(description, "`grok:"+model+"`") != 1 {
							t.Fatalf("missing or duplicated Grok model %q in guidance: %q", model, description)
						}
					}
				}

				property["description"] = native
			}
			properties["message"].(map[string]any)["encrypted"] = true
			if !bytes.Equal(responses.MustMarshalJSON(got.Parameters), responses.MustMarshalJSON(schema)) {
				t.Fatalf("native schema changed: %s", responses.MustMarshalJSON(got.Parameters))
			}
			arguments := `{"agent_type":"review","model":"grok:grok-4.6","fork_turns":"none","reasoning_effort":"high","message":"complete assignment","permission":"native"}`
			item := responses.MustMarshalJSON(map[string]any{"type": "function_call", "namespace": subagentBridgeNamespace, "name": "spawn_agent", "arguments": arguments, "call_id": "spawn"})
			check := func(raw json.RawMessage) {
				t.Helper()
				var restored map[string]json.RawMessage
				if err := json.Unmarshal(raw, &restored); err != nil {
					t.Fatal(err)
				}
				if responses.StringField(restored, "arguments") != arguments || responses.StringField(restored, "namespace") != "collaboration" {
					t.Fatalf("restoration changed arguments or namespace: %s", raw)
				}
			}
			result, err := bridge.TransformJSON(responses.MustMarshalJSON(map[string]any{"output": []json.RawMessage{item}}))
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
			events, err := bridge.TransformSSE(responses.MustMarshalJSON(map[string]any{"type": "response.output_item.done", "item": item}))
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
	if _, err := prepareSubagentBridge(&request, true, false); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(request.Fields["tools"], []byte(`"fork_turns":`)) ||
		bytes.Contains(request.Fields["tools"], []byte(`"reasoning_effort":`)) {
		t.Fatalf("projection added absent arguments: %s", request.Fields["tools"])
	}
}

func TestSubagentBridgeWithoutGrok(t *testing.T) {
	for _, additional := range []bool{false, true} {
		request := bridgeTestRequest(t, additional)
		bridge, err := prepareSubagentBridge(&request, false, false)
		if err != nil || bridge == nil {
			t.Fatalf("prepare ordinary bridge: %v", err)
		}
		wire := responses.MustMarshalJSON(request.Fields)
		if !bytes.Contains(wire, []byte(subagentBridgeNamespace)) || bytes.Contains(wire, []byte("grok:")) ||
			bytes.Contains(wire, []byte(`"encrypted":true`)) {
			t.Fatalf("ordinary projection exposes wrong contract: %s", wire)
		}
		if !bytes.Contains(wire, responses.MustMarshalJSON(collaborationNamespaceInstruction)) || responses.StringField(request.Fields, "instructions") != "keep" {
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
					fields["tools"] = json.RawMessage(base.Fields["tools"])
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
					provider, &output, testCriticalErrors(), proxy); err != nil {
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
	message := nativeAssignmentTestMessage("/root/child", "NEW_TASK", "")
	message["content"] = append(message["content"].([]any), map[string]any{"type": "encrypted_content", "encrypted_content": "opaque"})
	request.Fields["input"] = mustTestJSON(t, []any{encrypted, message, plain})
	if _, err := prepareSubagentBridge(&request, false, false); err != nil {
		t.Fatal(err)
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(request.Fields["input"], &input); err != nil {
		t.Fatal(err)
	}
	if !responses.SameJSONValue(responses.MustMarshalJSON(input[0]), responses.MustMarshalJSON(encrypted)) ||
		!responses.SameJSONValue(responses.MustMarshalJSON(input[1]), responses.MustMarshalJSON(message)) {
		t.Fatal("old encrypted call or assignment changed")
	}
	if responses.StringField(input[2], "namespace") != subagentBridgeNamespace ||
		responses.StringField(input[2], "call_id") != "new" ||
		responses.StringField(input[2], "arguments") != plain["arguments"] ||
		len(input[2]["encrypted_function_args"]) != 0 {
		t.Fatalf("plaintext replay not projected exactly: %s", responses.MustMarshalJSON(input[2]))
	}
}
