package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/responses"

	"github.com/coder/websocket"
)

// Captured from the auxiliary catalog in session
// 01a08b89-9256-7e50-bb37-38fe8f5b19d5; no prompt or session metadata is retained.
func auxiliaryCodeModeTools(t *testing.T) []any {
	t.Helper()
	data, err := os.ReadFile("testdata/auxiliary_code_mode_tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var tools []any
	if err := json.Unmarshal(data, &tools); err != nil {
		t.Fatal(err)
	}
	return tools
}

func titleRequestFields() map[string]any {
	return map[string]any{
		"model":        "gpt-test",
		"instructions": "Generate a short title. Keep these instructions unchanged.",
		"input":        []any{map[string]any{"type": "message", "role": "user", "content": "Format Run previews"}},
		"text": map[string]any{"format": map[string]any{
			"type": "json_schema", "name": "title", "strict": true,
			"schema": map[string]any{"type": "object", "properties": map[string]any{
				"title": map[string]any{"type": "string"},
			}, "required": []string{"title"}, "additionalProperties": false},
		}},
	}
}

func TestExecuteAuxiliaryStructuredRequest(t *testing.T) {
	for _, catalog := range []string{"omitted", "null", "empty", "additional", "both", "code mode", "flat code mode", "top code mode", "top namespaced code mode", "helper code mode", "unstructured helper code mode", "native helper", "unstructured empty"} {
		t.Run(catalog, func(t *testing.T) {
			fields := titleRequestFields()
			switch catalog {
			case "null":
				fields["tools"] = nil
			case "empty", "both":
				fields["tools"] = []any{}
			}
			if catalog == "additional" || catalog == "both" {
				fields["input"] = append([]any{map[string]any{"type": "additional_tools", "tools": []any{}}}, fields["input"].([]any)...)
			}
			if strings.HasPrefix(catalog, "unstructured ") {
				delete(fields, "text")
			}
			if catalog == "native helper" {
				fields["tools"] = []any{map[string]any{"type": "function", "name": "lookup"}}
			}
			if strings.Contains(catalog, "code mode") {
				tools := auxiliaryCodeModeTools(t)
				if strings.Contains(catalog, "helper") {
					nested := tools[0].(map[string]any)["tools"].([]any)
					exec := nested[0].(map[string]any)
					exec["description"] = exec["description"].(string) + "\n\n### clock__curr_time\nReturn the current time.\n\nexec tool declaration:\n```ts\ndeclare const tools: { clock__curr_time(args: {}): Promise<{ current_time: string; }>; };\n```"
					tools[0].(map[string]any)["tools"] = append(nested, map[string]any{"type": "function", "name": "request_user_input_async"})
					tools = append(tools,
						map[string]any{"type": "namespace", "name": "clock", "tools": []any{map[string]any{"type": "function", "name": "sleep"}}},
						map[string]any{"type": "namespace", "name": "mekugi_collaboration", "tools": []any{map[string]any{"type": "function", "name": "spawn_agent"}}})
				}
				if catalog == "flat code mode" || catalog == "top code mode" {
					tools = tools[0].(map[string]any)["tools"].([]any)
				}
				if strings.HasPrefix(catalog, "top ") {
					fields["tools"] = tools
				} else {
					fields["input"] = append([]any{map[string]any{"type": "additional_tools", "tools": tools}}, fields["input"].([]any)...)
				}
			}
			request, err := parseResponsesRequest(mustTestJSON(t, fields))
			if err != nil {
				t.Fatal(err)
			}
			response := `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"title\":\"Format Run previews\"}"}]}]}`
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(response)}}}
			proxy := newManagedMekugiProxy(t)
			var output bytes.Buffer
			err = executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", nil), "title-session", provider, &output, nil, proxy, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 1 || !sameJSONValue(provider.forwarded[0], mustTestJSON(t, fields)) {
				t.Fatalf("structured request changed: %s", provider.forwarded)
			}
			if !sameJSONValue(output.Bytes(), []byte(response)) {
				t.Fatalf("structured response changed: %s", output.Bytes())
			}
		})
	}
}

func TestAuxiliaryCodeModeDoesNotAdmitOtherTools(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]any) []any
	}{
		{"nested executor declaration", func(tools []any) []any {
			exec := tools[0].(map[string]any)
			exec["description"] = exec["description"].(string) + "\n\n### exec_command\nRun a command in a PTY."
			return tools
		}},
		{"malformed editing declaration", func(tools []any) []any {
			tools[0].(map[string]any)["description"] = "### `apply_patch`\nMalformed editing declaration."
			return tools
		}},
		{"wrong exec kind", func(tools []any) []any {
			tools[0].(map[string]any)["type"] = "function"
			return tools
		}},
		{"duplicate exec", func(tools []any) []any { return append(tools, tools[0]) }},
		{"wait without exec", func(tools []any) []any { return tools[1:] }},
		{"native executor", func(tools []any) []any {
			return append(tools, map[string]any{"type": "function", "name": "exec_command"})
		}},
		{"malformed nested tool", func(tools []any) []any { return append(tools, nil) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			tools := auxiliaryCodeModeTools(t)
			namespace := tools[0].(map[string]any)
			namespace["tools"] = test.mutate(namespace["tools"].([]any))
			fields := titleRequestFields()
			fields["input"] = []any{map[string]any{"type": "additional_tools", "tools": tools}}
			request, err := parseResponsesRequest(mustTestJSON(t, fields))
			if err != nil {
				t.Fatal(err)
			}
			provider := &serverFakeProvider{}
			err = executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", nil), "title-session", provider, io.Discard, nil, newManagedMekugiProxy(t), nil)
			if err == nil || len(provider.forwarded) != 0 {
				t.Fatalf("unsupported catalog admitted: error=%v forwards=%d", err, len(provider.forwarded))
			}
		})
	}
}

func TestStructuredRequestWithEditingToolsStillRewrites(t *testing.T) {
	bareTools := auxiliaryCodeModeTools(t)[0].(map[string]any)["tools"].([]any)
	preamble := bareTools[0].(map[string]any)["description"].(string)
	for _, client := range []struct{ name, description string }{
		{"App", testCodeModeDescription}, {"CLI", testCLICodeModeDescription},
	} {
		for _, layout := range []string{"top", "additional", "namespaced additional"} {
			t.Run(client.name+"/"+layout, func(t *testing.T) {
				// Use the same bare preamble as the auxiliary request, with real
				// nested editing declarations in each client's supported format.
				description := preamble + client.description[strings.Index(client.description, "\n\n###"):]
				additional := testCodeModeAdditionalTools(description)
				namespace := additional["tools"].([]any)[0].(map[string]any)
				tools := namespace["tools"].([]any)
				fields := titleRequestFields()
				switch layout {
				case "top":
					fields["tools"] = tools
				case "additional":
					fields["input"] = append([]any{map[string]any{"type": "additional_tools", "tools": tools}}, fields["input"].([]any)...)
				case "namespaced additional":
					additional["tools"] = []any{namespace}
					fields["input"] = append([]any{additional}, fields["input"].([]any)...)
				}
				request, err := parseResponsesRequest(mustTestJSON(t, fields))
				if err != nil {
					t.Fatal(err)
				}
				provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
				proxy := newManagedMekugiProxy(t)
				err = executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", nil), "session", provider, io.Discard, nil, proxy, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(provider.forwarded) != 1 || !bytes.Contains(provider.forwarded[0], []byte(`"name":"shell"`)) {
					t.Fatal("structured editing request bypassed HPATCH tool rewriting")
				}
			})
		}
	}
}

func TestToolFreeStructuredRequestDoesNotBypassAdmission(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any, http.Header)
	}{
		{"partial native executor", func(f map[string]any, _ http.Header) {
			f["tools"] = []any{map[string]any{"type": "function", "name": "exec_command"}}
		}},
		{"malformed tools", func(f map[string]any, _ http.Header) { f["tools"] = "invalid" }},
		{"partial additional executor", func(f map[string]any, _ http.Header) {
			f["input"] = []any{map[string]any{"type": "additional_tools", "tools": []any{map[string]any{"type": "function", "name": "exec_command"}}}}
		}},
		{"malformed additional tools", func(f map[string]any, _ http.Header) {
			f["input"] = []any{map[string]any{"type": "additional_tools", "tools": "invalid"}}
		}},
		{"missing metadata", func(_ map[string]any, h http.Header) { h.Del(codexTurnMetadataHeader) }},
		{"missing thread", func(_ map[string]any, h http.Header) { h.Del(threadIDHeader) }},
		{"generating prewarm", func(_ map[string]any, h http.Header) { h.Set(codexTurnMetadataHeader, `{"request_kind":"prewarm"}`) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fields := titleRequestFields()
			headers := serverMetadataHeaders(t, "turn", nil)
			test.mutate(fields, headers)
			request, err := parseResponsesRequest(mustTestJSON(t, fields))
			if err != nil {
				t.Fatal(err)
			}
			provider := &serverFakeProvider{}
			err = executeRequest(t.Context(), t.Context(), request, headers, "title-session", provider, io.Discard, nil, newManagedMekugiProxy(t), nil)
			if err == nil || len(provider.forwarded) != 0 {
				t.Fatalf("invalid request admitted: error=%v forwards=%d", err, len(provider.forwarded))
			}
		})
	}
}

func TestResponsesWebSocketToolFreeStructuredTurnAfterPrewarm(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	headers := codexAuthHeaders()
	headers.Set(sessionIDHeader, "title-session")
	headers.Set(threadIDHeader, "title-thread")
	proxy := newManagedMekugiProxy(t)
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.CloseNow()
		for _, id := range []string{"warm", "title"} {
			request, err := providerSocketRead(ctx, upstream)
			if err != nil {
				t.Error(err)
				return
			}
			if id == "title" {
				if jsonString(request, "previous_response_id") != "warm" || !sameJSONValue(request["text"], mustTestJSON(t, titleRequestFields()["text"])) {
					t.Errorf("title continuation changed: %s", mustMarshalJSON(request))
				}
				if len(request["tools"]) != 0 {
					t.Error("title request acquired tools")
				}
			}
			if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", id)); err != nil {
				t.Error(err)
				return
			}
		}
		_, _, _ = upstream.Read(ctx)
	}), proxy, headers)
	metadata := func(kind string) map[string]string {
		return map[string]string{codexTurnMetadataHeader: string(mustMarshalJSON(codexTurnMetadata{RequestKind: responses.RequestKind(kind)}))}
	}
	socketWrite(t, ctx, conn, map[string]any{
		"type": "response.create", "model": "gpt-test", "generate": false,
		"input":           []any{map[string]any{"type": "additional_tools", "tools": auxiliaryCodeModeTools(t)}},
		"client_metadata": metadata("prewarm"),
	})
	if event := socketRead(t, ctx, conn); jsonString(event, "type") != "response.completed" {
		t.Fatalf("prewarm failed: %s", mustMarshalJSON(event))
	}
	fields := titleRequestFields()
	fields["type"] = "response.create"
	fields["previous_response_id"] = "warm"
	fields["client_metadata"] = metadata("turn")
	socketWrite(t, ctx, conn, fields)
	if event := socketRead(t, ctx, conn); jsonString(event, "type") != "response.completed" {
		t.Fatalf("title turn failed: %s", mustMarshalJSON(event))
	}
}

// Keep the cross-client contract in one matrix: changing schemas or adding
// helper tools must not turn a non-editing request into an editing request, and
// neither client's real editing declarations may pass through unmodified.
func TestToolProjectionAcrossClients(t *testing.T) {
	for _, client := range []struct{ name, description string }{
		{"App", testCodeModeDescription},
		{"CLI", testCLICodeModeDescription},
		{"App changed preamble", "Different client introduction." + testCodeModeDescription[strings.Index(testCodeModeDescription, "\n\n###"):]},
		{"CLI absent preamble", testCLICodeModeDescription[strings.Index(testCLICodeModeDescription, "\n\n###"):]},
	} {

		for _, layout := range []string{"top", "additional", "namespaced additional"} {
			for _, schema := range []bool{false, true} {
				for _, editing := range []bool{false, true} {
					name := fmt.Sprintf("%s/%s/schema=%t/editing=%t", client.name, layout, schema, editing)
					t.Run(name, func(t *testing.T) {
						fields := titleRequestFields()
						if !schema {
							delete(fields, "text")
						}
						description := client.description
						if !editing {
							// Retain each client's own preamble and generic executor
							// example, but advertise only a clock helper.
							description = description[:strings.Index(description, "\n\n###")] +
								"\n\n### `clock__curr_time`\nReturn the current time.\n\nexec tool declaration:\n```ts\ndeclare const tools: { clock__curr_time(args: {}): Promise<unknown>; };\n```"
						}
						tools := []any{
							map[string]any{"type": "custom", "name": "exec", "description": description},
							map[string]any{"type": "function", "name": "wait"},
							map[string]any{"type": "function", "name": "request_user_input_async"},
						}
						switch layout {
						case "top":
							fields["tools"] = tools
						case "additional":
							fields["input"] = append([]any{map[string]any{"type": "additional_tools", "tools": tools}}, fields["input"].([]any)...)
						case "namespaced additional":
							fields["input"] = append([]any{map[string]any{"type": "additional_tools", "tools": []any{
								map[string]any{"type": "namespace", "name": "functions", "tools": tools},
								map[string]any{"type": "namespace", "name": "clock", "tools": []any{map[string]any{"type": "function", "name": "sleep"}}},
								map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{map[string]any{"type": "function", "name": "spawn_agent"}}},
							}}}, fields["input"].([]any)...)
						}
						request, err := parseResponsesRequest(mustTestJSON(t, fields))
						if err != nil {
							t.Fatal(err)
						}
						provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
						proxy := newManagedMekugiProxy(t)
						err = executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", nil), "session", provider, io.Discard, nil, proxy, nil)
						if err != nil {
							t.Fatal(err)
						}
						if len(provider.forwarded) != 1 {
							t.Fatal("request not forwarded")
						}
						if editing {
							if !bytes.Contains(provider.forwarded[0], []byte(`"name":"shell"`)) {
								t.Fatal("editing request bypassed projection")
							}
						} else if !sameJSONValue(provider.forwarded[0], mustTestJSON(t, fields)) {
							t.Fatal("non-editing request was rewritten")
						}
					})
				}
			}
		}
	}
}
