package router

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func continuationTestCatalog() *responsesToolCatalog {
	return decodeResponsesToolCatalog(map[string]json.RawMessage{
		"input": mustMarshalJSON([]any{
			map[string]any{"type": "additional_tools", "tools": []any{
				map[string]any{"type": "namespace", "name": "functions", "tools": []any{
					map[string]any{"type": "custom", "name": "exec", "description": "### `write_stdin`\nexec tool declaration:\ndeclare const tools: { write_stdin(args: { session_id: number; chars?: string }): Promise<unknown>; };\n"},
					map[string]any{"type": "function", "name": "wait", "parameters": map[string]any{
						"type": "object", "properties": map[string]any{"cell_id": map[string]any{"type": "string"}},
					}},
				}},
			}},
		}),
	})
}

func continuationTestCall(name, id, input string) map[string]json.RawMessage {
	field, kind := "input", "custom_tool_call"
	if name == "wait" || name == "write_stdin" || name == "exec_command" {
		field, kind = "arguments", "function_call"
	}
	return map[string]json.RawMessage{
		"type": mustMarshalJSON(kind), "name": mustMarshalJSON(name),
		"call_id": mustMarshalJSON(id), field: mustMarshalJSON(input),
	}
}

func continuationTestOutput(id string, parts ...string) map[string]json.RawMessage {
	var content []any
	for _, part := range parts {
		content = append(content, map[string]any{"type": "input_text", "text": part})
	}
	return map[string]json.RawMessage{
		"type": mustMarshalJSON("custom_tool_call_output"), "call_id": mustMarshalJSON(id),
		"output": mustMarshalJSON(content),
	}
}

func TestNativeExecutionMetadataBoundary(t *testing.T) {
	for _, test := range []struct {
		text string
		id   int64
	}{
		{"Chunk ID: abc\nWall time: 0.1000 seconds\nProcess running with session ID 42\nOutput:\nhello", 42},
		{"Wall time: 0.1000 seconds\nProcess running with session ID 43\nOriginal token count: 500\nOutput:\nhello", 43},
		{"Chunk ID: abc\nWall time: 0.1000 seconds\nProcess running with session ID 42\nFinal output:\nhello", 42},
		{"Wall time: 0.1000 seconds\nProcess running with session ID 43\nOriginal token count: 500\nFinal output:\nhello", 43},
		{"Wall time: 0.1000 seconds\nProcess exited with code 0\nFinal output:\nProcess running with session ID 42\n", 0},
		{"Wall time: 0.1000 seconds\nProcess exited with code 0\nOutput:\nProcess running with session ID 42\n", 0},
		{"Process running with session ID 42\nOutput:\n", 0},
		{"Wall time: 0.1000 seconds\nProcess running with session ID -1\nOutput:\n", 0},
	} {
		if got := nativeExecutionSession(test.text); got != test.id {
			t.Errorf("nativeExecutionSession(%q)=%d, want %d", test.text, got, test.id)
		}
	}
}

func TestExecutionContinuationRetiresSuggestions(t *testing.T) {
	for _, kind := range []string{"cell", "session", "nested session"} {
		states := []string{"pending", "yielded", "completed"}
		if kind == "cell" {
			states = append(states, "failed", "terminated")
		}
		for _, state := range states {
			t.Run(kind+"/"+state, func(t *testing.T) {
				first := continuationTestCall("shell", "a", "sleep 100")
				yielded := "Script running with cell ID cell-17\nWall time 0.1 seconds\nOutput:\n"
				resume := continuationTestCall("wait", "b", `{"cell_id":"cell-17"}`)
				terminal := "Script completed\nWall time 0.1 seconds\nOutput:\n"
				if state == "failed" {
					terminal = "Script failed\nWall time 0.1 seconds\nOutput:\n"
				} else if state == "terminated" {
					terminal = "Script terminated\nWall time 0.1 seconds\nOutput:\n"
				}
				if kind != "cell" {
					first = continuationTestCall("exec_command", "a", `{"cmd":"sleep 100"}`)
					yielded = "Wall time: 0.1 seconds\nProcess running with session ID 42\nOutput:\nok test-package\n"
					resume = continuationTestCall("write_stdin", "b", `{"session_id":42,"chars":""}`)
					terminal = "Wall time: 0.1 seconds\nProcess exited with code 0\nOutput:\n"
					if kind == "nested session" {
						resume = continuationTestCall("exec", "b", `text(await tools.write_stdin({session_id:42,chars:""}));`)
						terminal = "Script completed\nWall time 0.1 seconds\nOutput:\n{\"output\":\"done\",\"exit_code\":0}"
					}
				}
				original := continuationTestOutput("a", yielded, "unrelated warning")
				history := map[string]mekugiHistory{"a": {ToolName: "shell", PluginID: builtinToolsPluginID}}
				if kind != "cell" {
					delete(history, "a")
				}
				request := parsedResponsesRequest{fields: map[string]json.RawMessage{
					"input": mustMarshalJSON([]any{first, original}),
				}}
				projectExecutionContinuations(&request, continuationTestCatalog(), "exec", history)
				if !strings.Contains(string(request.fields["input"]), "continuation") {
					t.Fatal("live execution lost its suggestion")
				}
				var items []map[string]json.RawMessage
				if err := json.Unmarshal(request.fields["input"], &items); err != nil {
					t.Fatal(err)
				}
				items = append(items, resume)
				if state != "pending" {
					output := terminal
					if state == "yielded" {
						output = yielded
						if kind == "nested session" {
							output = "Script completed\nWall time 0.1 seconds\nOutput:\n{\"output\":\"partial\",\"session_id\":42}"
						}
					}
					items = append(items, continuationTestOutput("b", output))
				}
				request.setInput(mustMarshalJSON(items))
				projectExecutionContinuations(&request, continuationTestCatalog(), "exec", history)
				if err := json.Unmarshal(request.fields["input"], &items); err != nil {
					t.Fatal(err)
				}
				if !sameJSONValue(items[1]["output"], original["output"]) {
					t.Fatalf("old suggestion or changed host output: %s", items[1]["output"])
				}
				want := 0
				if state == "yielded" {
					want = 1
				}
				if got := strings.Count(string(request.fields["input"]), `\"continuation\"`); got != want {
					t.Fatalf("got %d suggestions, want %d: %s", got, want, request.fields["input"])
				}
				previous := string(request.fields["input"])
				projectExecutionContinuations(&request, continuationTestCatalog(), "exec", history)
				if string(request.fields["input"]) != previous {
					t.Fatal("projection is not idempotent")
				}
			})
		}
	}
}

func TestExecutionContinuationRetiresAcrossProjectionChanges(t *testing.T) {
	for _, source := range []string{
		`text(await tools.write_stdin({session_id:42}));`,
		`const result = await tools.write_stdin({session_id:42}); text(result.output);`,
		`await tools.write_stdin({session_id:42});`,
	} {
		for _, outputOnlyReplay := range []bool{false, true} {
			t.Run(source+"/output-only="+strconv.FormatBool(outputOnlyReplay), func(t *testing.T) {
				original := continuationTestOutput("a", "Wall time: 0.1 seconds\nProcess running with session ID 42\nOutput:\n", "unrelated warning")
				request := parsedResponsesRequest{fields: map[string]json.RawMessage{
					"input": mustMarshalJSON([]any{
						continuationTestCall("exec_command", "a", `{"cmd":"sleep 100"}`), original,
						continuationTestCall("exec", "other", `await unrelated();`),
						continuationTestOutput("other", "Script running with cell ID other-cell\nWall time 0.1 seconds\nOutput:\n"),
					}),
				}}
				projectExecutionContinuations(&request, continuationTestCatalog(), "exec", nil)
				var items []map[string]json.RawMessage
				if err := json.Unmarshal(request.fields["input"], &items); err != nil {
					t.Fatal(err)
				}
				resume := continuationTestCall("exec", "b", source)
				history := map[string]mekugiHistory{}
				if outputOnlyReplay {
					history["b"] = mekugiHistory{UpstreamItem: resume, Script: source, ToolName: "exec"}
				} else {
					items = append(items, resume)
				}
				items = append(items, continuationTestOutput("b", "Script completed\nWall time 0.1 seconds\nOutput:\ndone"))
				request.setInput(mustMarshalJSON(items))
				// The old nested next_call cannot be reconstructed from this catalog.
				catalog := decodeResponsesToolCatalog(map[string]json.RawMessage{"tools": mustMarshalJSON([]any{})})
				projectExecutionContinuations(&request, catalog, "exec", history)
				if err := json.Unmarshal(request.fields["input"], &items); err != nil {
					t.Fatal(err)
				}
				if !sameJSONValue(items[1]["output"], original["output"]) {
					t.Fatalf("retired annotation survived catalog change: %s", items[1]["output"])
				}
				if !strings.Contains(string(items[3]["output"]), "continuation") {
					t.Fatal("unrelated outstanding cell lost its suggestion")
				}
			})
		}
	}
}

func TestNativeContinuationUsesAvailableDirectTool(t *testing.T) {
	catalog := decodeResponsesToolCatalog(map[string]json.RawMessage{
		"tools": mustMarshalJSON([]any{
			map[string]any{"type": "function", "name": "write_stdin", "parameters": map[string]any{
				"type": "object", "properties": map[string]any{
					"session_id": map[string]any{"type": "number"}, "chars": map[string]any{"type": "string"},
				},
			}},
		}),
	})
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"input": mustMarshalJSON([]any{
			continuationTestCall("exec_command", "native", `{"cmd":"sleep 100"}`),
			map[string]any{
				"type": "function_call_output", "call_id": "native",
				"output": "Wall time: 0.1000 seconds\nProcess running with session ID 42\nOutput:\n",
			},
		}),
	}}
	projectExecutionContinuations(&request, catalog, "exec_command", nil)
	var items []map[string]json.RawMessage
	_ = json.Unmarshal(request.fields["input"], &items)
	texts := executionOutputTexts(items[1]["output"])
	if len(texts) != 2 || !strings.Contains(texts[1], `"tool":"write_stdin"`) ||
		!strings.Contains(texts[1], `"input":{"chars":"","session_id":42}`) {
		t.Fatalf("native action = %v", texts)
	}
}

func TestMissingContinuationToolDoesNotInventAuthority(t *testing.T) {
	tools := executionContinuationTools{}
	for _, continuation := range []executionContinuation{tools.forCell("cell-1"), tools.forSession(42)} {
		if continuation.NextCall != nil || continuation.Reason == "" {
			t.Fatalf("invented continuation: %+v", continuation)
		}
	}
}

func TestExecutionContinuationLongWaitTiming(t *testing.T) {
	for _, name := range []string{"wait", "write_stdin"} {
		for _, limit := range []struct {
			maximum json.Number
			want    int
		}{
			{"0", 300000},
			{"120000", 120000},
			{"120000.0", 120000},
			{"120000.5", 120000},
			{"300000", 300000},
			{"600000", 300000},
		} {
			t.Run(name+"/"+string(limit.maximum), func(t *testing.T) {
				properties := map[string]any{
					"cell_id":       map[string]any{"type": "string"},
					"session_id":    map[string]any{"type": "integer"},
					"chars":         map[string]any{"type": "string"},
					"yield_time_ms": map[string]any{"type": "integer", "maximum": limit.maximum},
					"unrelated":     map[string]any{"type": "number", "maximum": 1.5},
				}
				catalog := decodeResponsesToolCatalog(map[string]json.RawMessage{
					"tools": mustMarshalJSON([]any{map[string]any{
						"type": "function", "name": name,
						"parameters": map[string]any{"type": "object", "properties": properties},
					}}),
				})
				tools := executionTools(catalog, "exec")
				result := tools.forCell("cell-1")
				if name == "write_stdin" {
					result = tools.forSession(42)
				}
				want := limit.want
				if result.NextCall == nil || result.NextCall.Input.(map[string]any)["yield_time_ms"] != want {
					t.Fatalf("continuation = %+v, want wait %d", result, want)
				}
			})
		}
	}
}

func TestExecutionContinuationNestedLongWait(t *testing.T) {
	description := "### `write_stdin`\ndeclare const tools: { write_stdin(args: { session_id: number; chars?: string; yield_time_ms?: number }): Promise<unknown>; };\n"
	catalog := decodeResponsesToolCatalog(map[string]json.RawMessage{
		"tools": mustMarshalJSON([]any{
			map[string]any{"type": "custom", "name": "exec", "description": description},
		}),
	})
	result := executionTools(catalog, "exec").forSession(42)
	want := "// @exec: {\"yield_time_ms\":300000}\n" + `text(await tools.write_stdin({"chars":"","session_id":42,"yield_time_ms":300000}));`
	if result.NextCall == nil || result.NextCall.Input != want {
		t.Fatalf("nested continuation = %+v, want %s", result, want)
	}
}
