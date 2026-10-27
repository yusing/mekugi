package responses

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestTranslationExecGuidance(t *testing.T) {
	for _, tc := range []struct {
		name, namespace string
		additional      bool
		want            bool
	}{
		{name: "bare", want: true},
		{name: "namespaced", namespace: "functions", want: true},
		{name: "additional", namespace: "functions", additional: true, want: true},
		{name: "unrelated", namespace: "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := []any{map[string]any{"type": "custom", "name": "exec", "description": "original"}}
			if tc.namespace != "" {
				tools = []any{map[string]any{"type": "namespace", "name": tc.namespace, "tools": tools}}
			}
			request := map[string]any{"tools": tools, "input": []any{}}
			if tc.additional {
				delete(request, "tools")
				request["input"] = []any{map[string]any{"type": "additional_tools", "tools": tools}}
			}
			translated, err := testTranslate(mustTestJSON(t, request))
			if err != nil {
				t.Fatal(err)
			}
			tool := translated.body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
			want := "original"
			if tc.want {
				want += "\n" + execInstruction
			}
			if tool["description"] != want {
				t.Fatalf("description = %q, want %q", tool["description"], want)
			}
		})
	}
}

func TestGrokTranslationPreservesToolsHistoryAndImages(t *testing.T) {
	body := grokTestRequest(t, true)
	var request map[string]any
	_ = json.Unmarshal(body, &request)
	request["input"] = []any{
		map[string]any{"type": "message", "role": "developer", "content": "rules"},
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": "task"}}},
		map[string]any{"type": "custom_tool_call", "call_id": "c1", "name": "exec", "input": "text(1)"},
		map[string]any{"type": "function_call", "call_id": "c2", "namespace": "other", "name": "lookup", "arguments": "{}"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "c1", "output": []any{map[string]string{"type": "input_text", "text": "image"}, map[string]string{"type": "input_image", "image_url": "data:image/png;base64,AA=="}}},
		map[string]any{"type": "function_call_output", "call_id": "c2", "output": "result"},
	}
	tr, err := testTranslate(mustTestJSON(t, request))
	if err != nil {
		t.Fatal(err)
	}
	messages := tr.body["messages"].([]map[string]any)
	if messages[1]["role"] != "system" || len(messages[3]["tool_calls"].([]any)) != 2 {
		t.Fatalf("messages=%v", messages)
	}
	if messages[4]["role"] != "tool" || messages[4]["tool_call_id"] != "c1" || len(messages[4]["content"].([]any)) != 2 {
		t.Fatalf("tool image=%v", messages[4])
	}
	if tr.body["reasoning_effort"] != "low" {
		t.Fatal("reasoning lost")
	}
	for _, kind := range []string{"encrypted_content", "input_file"} {
		request["input"] = []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": kind, "encrypted_content": "opaque"}}}}
		if _, err := testTranslate(mustTestJSON(t, request)); err == nil {
			t.Fatalf("accepted %s", kind)
		}
	}
	request["input"] = []any{map[string]string{"type": "reasoning", "encrypted_content": "opaque"}}
	if _, err := testTranslate(mustTestJSON(t, request)); err == nil {
		t.Fatal("accepted encrypted reasoning")
	}
}

func TestGrokStreamingCustomCallsAndUsage(t *testing.T) {
	tr, err := testTranslate(grokTestRequest(t, true))
	if err != nil {
		t.Fatal(err)
	}
	wire := providerToolName("", "exec")
	call := func(args string) any {
		return map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "c1", "function": map[string]string{"name": wire, "arguments": args}}}}}}}
	}
	stream := grokTestSSE(call(`{"input":"text(1)"}`), map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}})
	var events []map[string]any
	result, err := tr.codec.Read(strings.NewReader(stream), func(e map[string]any) error { events = append(events, e); return nil }, testFailureDetail)
	if err != nil {
		t.Fatal(err)
	}
	output := result["output"].([]any)
	item := output[0].(map[string]any)
	if item["type"] != "custom_tool_call" || item["input"] != "text(1)" || item["call_id"] != "c1" {
		t.Fatalf("item=%v", item)
	}
	if events[1]["type"] != "response.in_progress" {
		t.Fatal("no progress while buffering arguments")
	}
	for _, bad := range []string{strings.TrimSuffix(stream, "data: [DONE]\n\n"), grokTestSSE(call(`{"bad":1}`), map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls"}}})} {
		var executed bool
		_, err := tr.codec.Read(strings.NewReader(bad), func(e map[string]any) error {
			if e["type"] == "response.output_item.done" {
				executed = true
			}
			return nil
		}, testFailureDetail)
		if err == nil || executed {
			t.Fatalf("bad stream exposed tool: err=%v executed=%v", err, executed)
		}
	}
	result, err = tr.codec.Read(strings.NewReader(grokTextStream()), func(map[string]any) error { return nil }, testFailureDetail)
	if err != nil {
		t.Fatal(err)
	}
	usage := result["usage"].(map[string]any)
	if usage["input_tokens"] != int64(10) || usage["output_tokens"] != int64(6) {
		t.Fatalf("usage=%v", usage)
	}
}

func TestGrokParallelToolLifecycle(t *testing.T) {
	tr := &Stream{policy: testPolicy(), endpoint: chatEndpoint{}, tools: map[string]providerTool{
		"custom":   {kind: "custom", name: "exec"},
		"function": {kind: "function", namespace: "functions", name: "lookup"},
	}}
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			args := `{"query":"test"}`
			if invalid {
				args = `{"query":`
			}
			stream := grokTestSSE(map[string]any{"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{
					"content": "Checking.",
					"tool_calls": []any{
						map[string]any{"index": 0, "id": "c1", "function": map[string]string{"name": "custom", "arguments": `{"input":"text(1)"}`}},
						map[string]any{"index": 1, "id": "c2", "function": map[string]string{"name": "function", "arguments": args}},
					},
				}, "finish_reason": "tool_calls",
			}}})
			var events []map[string]any
			result, err := tr.Read(strings.NewReader(stream), func(event map[string]any) error {
				if item, ok := event["item"].(map[string]any); ok && item["type"] == "message" {
					return nil
				}
				if event["type"] == "response.output_item.added" || event["type"] == "response.output_item.done" ||
					event["type"] == "response.custom_tool_call_input.done" || event["type"] == "response.function_call_arguments.done" {
					events = append(events, event)
				}
				return nil
			}, testFailureDetail)
			if invalid {
				if err == nil || len(events) != 0 {
					t.Fatalf("invalid parallel call released tools: events=%v err=%v", events, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 6 {
				t.Fatalf("tool events = %v", events)
			}
			for i, field := range []string{"input", "arguments"} {
				added, done, completed := events[i*3], events[i*3+1], events[i*3+2]
				item := result["output"].([]any)[i+1].(map[string]any)
				addedItem := added["item"].(map[string]any)
				doneType := "response.function_call_arguments.done"
				if field == "input" {
					doneType = "response.custom_tool_call_input.done"
				}
				if added["type"] != "response.output_item.added" || done["type"] != doneType ||
					completed["type"] != "response.output_item.done" ||
					addedItem[field] != "" || addedItem["status"] != "in_progress" ||
					done[field] != item[field] || done["item_id"] != item["id"] || done["call_id"] != item["call_id"] {
					t.Fatalf("invalid lifecycle: %v", events[i*3:i*3+3])
				}
				if field == "arguments" {
					if done["name"] != item["name"] {
						t.Fatalf("function completion name = %v, want %v", done["name"], item["name"])
					}
				} else if _, exists := done["name"]; exists {
					t.Fatalf("custom completion unexpectedly includes name: %v", done)
				}
				for _, event := range events[i*3 : i*3+3] {
					if event["output_index"] != i+1 {
						t.Fatalf("output index = %v", event["output_index"])
					}
					if eventItem, ok := event["item"].(map[string]any); ok &&
						(eventItem["id"] != item["id"] || eventItem["call_id"] != item["call_id"]) {
						t.Fatalf("changed identity: %v", eventItem)
					}
				}
			}
		})
	}
}

func TestGrokReasoningUsage(t *testing.T) {
	for _, fields := range []string{
		`"completion_tokens_details":{"reasoning_tokens":94}`,
		`"reasoning_tokens":94`,
		`"reasoning_tokens":94,"completion_tokens_details":{"reasoning_tokens":94}`,
	} {
		tr, err := testTranslate(grokTestRequest(t, true))
		if err != nil {
			t.Fatal(err)
		}
		stream := `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":32,"completion_tokens":9,"total_tokens":135,` + fields + "}}\n\ndata: [DONE]\n\n"
		result, err := tr.codec.Read(strings.NewReader(stream), func(map[string]any) error { return nil }, testFailureDetail)
		if err != nil {
			t.Fatal(err)
		}
		usage := result["usage"].(map[string]any)
		if usage["output_tokens"] != int64(103) || usage["total_tokens"] != int64(135) || usage["output_tokens_details"].(map[string]any)["reasoning_tokens"] != int64(94) {
			t.Fatalf("usage=%v", usage)
		}
	}
}

func TestGrokNullCustomInputIsNotExecutable(t *testing.T) {
	tr, err := testTranslate(grokTestRequest(t, true))
	if err != nil {
		t.Fatal(err)
	}
	stream := grokTestSSE(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "c", "function": map[string]string{"name": "exec", "arguments": `{"input":null}`}}}}, "finish_reason": "tool_calls"}}})
	executable := false
	_, err = tr.codec.Read(strings.NewReader(stream), func(event map[string]any) error {
		if event["type"] == "response.output_item.done" {
			executable = true
		}
		return nil
	}, testFailureDetail)
	if err == nil || executable {
		t.Fatalf("null input accepted: err=%v executable=%v", err, executable)
	}
}

func TestGrokRejectsNonStringHistory(t *testing.T) {
	for _, value := range []any{nil, 42, true, map[string]any{}} {
		for _, item := range []map[string]any{
			{"type": "message", "role": "user", "content": value},
			{"type": "function_call_output", "call_id": "c1", "output": value},
			{"type": "custom_tool_call_output", "call_id": "c1", "output": value},
			{"type": "custom_tool_call", "call_id": "c1", "name": "exec", "input": value},
		} {
			body := mustTestJSON(t, map[string]any{"model": grokModel, "input": []any{item}})
			if _, err := testTranslate(body); err == nil {
				t.Fatalf("accepted %s", body)
			}
		}
	}
	for _, value := range []any{"", []any{}} {
		if _, err := providerContent(mustTestJSON(t, value), testPolicy().Diagnostics); err != nil {
			t.Fatalf("rejected empty content: %v", err)
		}
	}
}

func TestGrokStreamCRLF(t *testing.T) {
	tr, err := testTranslate(grokTestRequest(t, true))
	if err != nil {
		t.Fatal(err)
	}
	result, err := tr.codec.Read(strings.NewReader(strings.ReplaceAll(grokTextStream(), "\n", "\r\n")), func(map[string]any) error { return nil }, testFailureDetail)
	if err != nil || result["status"] != "completed" {
		t.Fatalf("result=%v err=%v", result, err)
	}
}
