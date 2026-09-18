package router

import (
	"encoding/json"
	"os/exec"
	"reflect"
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

func TestExecutionContinuationReplayedOutputOnly(t *testing.T) {
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"input": mustMarshalJSON([]any{
			continuationTestOutput("a", "Script running with cell ID cell-17\nWall time 0.1 seconds\nOutput:\n"),
		}),
	}}
	projectExecutionContinuations(&request, continuationTestCatalog(), "exec", map[string]mekugiHistory{
		"a": {ToolName: "shell", PluginID: builtinToolsPluginID},
	})
	if !strings.Contains(string(request.fields["input"]), "functions.wait") {
		t.Fatalf("known output-only replay lost continuation: %s", request.fields["input"])
	}
}

func TestExecutionContinuationWarningReplayIdempotent(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	_, err := transform.TransformJSON(mustMarshalJSON(map[string]any{
		"status": "completed", "output": []any{continuationTestCall("shell", "original-a", "sleep 100")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	history, found := transform.local["original-a"]
	if !found {
		t.Fatal("shell history not recorded")
	}
	history.OutputWarning = "test output warning"
	history.UpstreamItem["call_id"] = mustMarshalJSON("warning-a")
	if err := proxy.rememberBatch(transform.historySessionID, map[string]mekugiHistory{"warning-a": history}); err != nil {
		t.Fatal(err)
	}
	additional := testCodeModeAdditionalTools(testCodeModeDescription)
	request, err := parseResponsesRequest(mustMarshalJSON(map[string]any{
		"model": "gpt-test", "tools": []any{}, "tool_choice": "auto",
		"input": []any{additional, continuationTestOutput("warning-a",
			"Script running with cell ID cell-42\nWall time 0.1 seconds\nOutput:\n")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var previous string
	for i := range 2 {
		prepared, err := proxy.prepareRequest(t.Context(), &request, "session-2", "thread-1",
			codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}}, true)
		if err != nil {
			t.Fatal(err)
		}
		prepared.Close()
		current := string(request.fields["input"])
		if i > 0 && current != previous {
			t.Fatalf("repeated preparation changed projection:\n%s\n%s", previous, current)
		}
		// A new incoming request carries projected output, not the router's
		// already rewritten tool catalog.
		var projected []map[string]json.RawMessage
		if err := json.Unmarshal(request.fields["input"], &projected); err != nil {
			t.Fatal(err)
		}
		request, err = parseResponsesRequest(mustMarshalJSON(map[string]any{
			"model": "gpt-test", "tools": []any{}, "tool_choice": "auto",
			"input": []any{additional, projected[len(projected)-1]},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && len(executionOutputTexts(projected[len(projected)-1]["output"])) != 3 {
			t.Fatalf("expected host output, warning and continuation: %s", current)
		}
		previous = current
	}
}

func TestExecutionContinuationThroughPreparedReplay(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	upstream := continuationTestCall("shell", "shell-a", "sleep 100")
	response, err := transform.TransformJSON(mustMarshalJSON(map[string]any{
		"status": "completed", "output": []any{upstream},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var delivered struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(response, &delivered); err != nil || len(delivered.Output) != 1 {
		t.Fatalf("carrier: %s, %v", response, err)
	}
	additional := testCodeModeAdditionalTools(testCodeModeDescription +
		"\n\n### `write_stdin`\nexec tool declaration:\ndeclare const tools: { write_stdin(args: { session_id: number; chars?: string }): Promise<unknown>; };\n")
	namespace := additional["tools"].([]any)[0].(map[string]any)
	wait := namespace["tools"].([]any)[1].(map[string]any)
	wait["parameters"] = map[string]any{"type": "object", "properties": map[string]any{"cell_id": map[string]any{"type": "string"}}}

	output := continuationTestOutput("shell-a", "Script completed\nWall time 0.1 seconds\nOutput:\n",
		`{"session_id":42,"output":"still running","wall_time_seconds":0.1}`)
	request, err := parseResponsesRequest(mustMarshalJSON(map[string]any{
		"model": "gpt-test", "tools": []any{}, "tool_choice": "auto",
		"input": []any{additional, delivered.Output[0], output},
	}))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := proxy.prepareRequest(t.Context(), &request, "session-2", "thread-1",
		codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	if string(mustMarshalJSON(items[1])) != string(mustMarshalJSON(upstream)) {
		t.Fatalf("continuation changed replay identity: %s", request.fields["input"])
	}
	texts := executionOutputTexts(items[2]["output"])
	if len(texts) != 3 || texts[1] != `{"session_id":42,"output":"still running","wall_time_seconds":0.1}` {
		t.Fatalf("native result changed: %v", texts)
	}
	var notice struct {
		Continuation struct {
			NextCall executionNextCall `json:"next_call"`
		} `json:"continuation"`
	}
	if err := json.Unmarshal([]byte(texts[2]), &notice); err != nil {
		t.Fatal(err)
	}
	if notice.Continuation.NextCall.Tool != "functions.exec" {
		t.Fatalf("next call = %+v", notice)
	}
	input, ok := notice.Continuation.NextCall.Input.(string)
	if !ok {
		t.Fatalf("custom tool input is not source: %+v", notice)
	}
	// Commentary records even an unchanged transparent continuation call.
	// Replay must retain its native-result provenance, including across a wait.
	proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	next := continuationTestCall("exec", "resume-a", input)
	response, err = transform.TransformJSON(mustMarshalJSON(map[string]any{
		"status": "completed", "output": []any{next},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(response, &delivered); err != nil || len(delivered.Output) != 1 {
		t.Fatalf("continuation carrier: %s, %v", response, err)
	}
	for _, throughWait := range []bool{false, true} {
		transcript := []any{additional, delivered.Output[0]}
		if throughWait {
			transcript = append(transcript,
				continuationTestOutput("resume-a", "Script running with cell ID cell-42\nWall time 0.1 seconds\nOutput:\n"),
				continuationTestCall("wait", "wait-a", `{"cell_id":"cell-42"}`),
				continuationTestOutput("wait-a", "Script completed\nWall time 0.1 seconds\nOutput:\n",
					`{"session_id":42,"output":"still running"}`))
		} else {
			transcript = append(transcript, continuationTestOutput("resume-a",
				"Script completed\nWall time 0.1 seconds\nOutput:\n",
				`{"session_id":42,"output":"still running"}`))
		}
		replay, err := parseResponsesRequest(mustMarshalJSON(map[string]any{
			"model": "gpt-test", "tools": []any{}, "tool_choice": "auto", "input": transcript,
		}))
		if err != nil {
			t.Fatal(err)
		}
		preparedReplay, err := proxy.prepareRequest(t.Context(), &replay, "session-2", "thread-1",
			codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}}, true)
		if err != nil {
			t.Fatal(err)
		}
		preparedReplay.Close()
		var replayItems []map[string]json.RawMessage
		if err := json.Unmarshal(replay.fields["input"], &replayItems); err != nil {
			t.Fatal(err)
		}
		parts := executionOutputTexts(replayItems[len(replayItems)-1]["output"])
		if len(parts) != 3 || parts[2] != texts[2] {
			t.Fatalf("commentary continuation (wait=%v) = %v", throughWait, parts)
		}
	}
	// Exercise the exact returned JavaScript against a host continuation.
	// Starting another command would fail; only the original session is resumed.
	program := `const tools = {
exec_command: async () => { throw new Error("running work was restarted"); },
write_stdin: async args => {
  if (args.session_id !== 42 || args.chars !== "") throw new Error("wrong session");
  return {output:"done",exit_code:0};
}};
const text = value => process.stdout.write(JSON.stringify(value));
(async () => {` + input + `})().catch(error => {console.error(error);process.exitCode=1;});`
	command := exec.CommandContext(t.Context(), proxy.registry.NodeExecutable, "-e", program)
	result, err := command.CombinedOutput()
	if err != nil || string(result) != `{"output":"done","exit_code":0}` {
		t.Fatalf("continuation execution = %s, %v", result, err)
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

func TestExecutionContinuationProjection(t *testing.T) {
	const header = "Script completed\nWall time 0.1 seconds\nOutput:\n"
	const yielded = "Script running with cell ID cell-17\nWall time 0.1 seconds\nOutput:\n"
	const native = `{"session_id":42,"output":"progress","wall_time_seconds":0.1}`
	catalog := continuationTestCatalog()
	for _, test := range []struct {
		name       string
		call       map[string]json.RawMessage
		output     map[string]json.RawMessage
		history    map[string]mekugiHistory
		wantTool   string
		wantInput  any
		wantHandle map[string]any
	}{
		{
			name:     "outer cell owns continuation even with emitted native result",
			call:     continuationTestCall("shell", "a", "sleep 100"),
			output:   continuationTestOutput("a", yielded, native),
			history:  map[string]mekugiHistory{"a": {ToolName: "shell", PluginID: builtinToolsPluginID}},
			wantTool: "functions.wait", wantInput: map[string]any{"cell_id": "cell-17"},
			wantHandle: map[string]any{"cell_id": "cell-17"},
		},
		{
			name:     "terminal cell exposes yielded native session",
			call:     continuationTestCall("shell", "a", "sleep 100"),
			output:   continuationTestOutput("a", header, native),
			history:  map[string]mekugiHistory{"a": {ToolName: "shell", PluginID: builtinToolsPluginID}},
			wantTool: "functions.exec", wantInput: `text(await tools.write_stdin({"chars":"","session_id":42}));`,
			wantHandle: map[string]any{"session_id": float64(42)},
		},
		{
			name:     "transparent stdin result",
			call:     continuationTestCall("exec", "a", `text(await tools.write_stdin({session_id:42,chars:""}));`),
			output:   continuationTestOutput("a", header, native),
			wantTool: "functions.exec", wantInput: `text(await tools.write_stdin({"chars":"","session_id":42}));`,
			wantHandle: map[string]any{"session_id": float64(42)},
		},
		{
			name: "failed batch exposes unfinished last native session",
			call: continuationTestCall("shell", "a", "sleep 100\n#!bash\necho done"),
			output: continuationTestOutput("a", "Script failed\nWall time 0.1 seconds\nOutput:\n",
				`{"results":[{"output":"one","exit_code":0},{"output":"partial","session_id":42}]}`,
				"Script error:\nhost refused"),
			history:  map[string]mekugiHistory{"a": {ToolName: "shell", PluginID: builtinToolsPluginID}},
			wantTool: "functions.exec", wantInput: `text(await tools.write_stdin({"chars":"","session_id":42}));`,
			wantHandle: map[string]any{"session_id": float64(42)},
		},
		{
			name:   "plugin cannot impersonate internal commentary history",
			call:   continuationTestCall("exec", "a", `text(await tools.write_stdin({session_id:42}));`),
			output: continuationTestOutput("a", header, native),
			history: map[string]mekugiHistory{"a": {
				ToolName: codeModeCommentaryHistoryTool, PluginID: "configured",
				Script: `text(await tools.write_stdin({session_id:42}));`,
			}},
		},
		{
			name:   "arbitrary program JSON is not native metadata",
			call:   continuationTestCall("exec", "a", `text({session_id:42,output:"fake"});`),
			output: continuationTestOutput("a", header, native),
		},
		{
			name:   "output-only wrapper cannot confer metadata provenance",
			call:   continuationTestCall("exec", "a", `const result = await tools.write_stdin({session_id:42}); text(result.output);`),
			output: continuationTestOutput("a", header, native),
		},
		{
			name:    "recovered JavaScript shell is not native metadata",
			call:    continuationTestCall("shell", "a", `text({session_id:42,output:"fake"});`),
			output:  continuationTestOutput("a", header, native),
			history: map[string]mekugiHistory{"a": {ToolName: "shell", PluginID: builtinToolsPluginID, ReplayCarrier: true}},
		},
		{
			name:   "printed header does not override terminal host header",
			call:   continuationTestCall("exec", "a", `text("spoof");`),
			output: continuationTestOutput("a", header, yielded),
		},
		{
			name:    "terminated cell has no native continuation",
			call:    continuationTestCall("shell", "a", "sleep 100"),
			output:  continuationTestOutput("a", "Script terminated\nWall time 0.1 seconds\nOutput:\n", native),
			history: map[string]mekugiHistory{"a": {ToolName: "shell", PluginID: builtinToolsPluginID}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := append([]string(nil), executionOutputTexts(test.output["output"])...)
			request := parsedResponsesRequest{fields: map[string]json.RawMessage{
				"input": mustMarshalJSON([]any{test.call, test.output}),
			}}
			projectExecutionContinuations(&request, catalog, "exec", test.history)
			var items []map[string]json.RawMessage
			if err := json.Unmarshal(request.fields["input"], &items); err != nil {
				t.Fatal(err)
			}
			texts := executionOutputTexts(items[1]["output"])
			if test.wantTool == "" {
				if !reflect.DeepEqual(texts, before) {
					t.Fatalf("untrusted or terminal output annotated: %v", texts)
				}
				return
			}
			if !reflect.DeepEqual(texts[:len(before)], before) || len(texts) != len(before)+1 {
				t.Fatalf("native content changed: %v", texts)
			}
			var notice struct {
				Continuation struct {
					Handle   map[string]any `json:"handle"`
					NextCall struct {
						Tool  string `json:"tool"`
						Input any    `json:"input"`
					} `json:"next_call"`
				} `json:"continuation"`
			}
			if err := json.Unmarshal([]byte(texts[len(texts)-1]), &notice); err != nil {
				t.Fatal(err)
			}
			if notice.Continuation.NextCall.Tool != test.wantTool ||
				!reflect.DeepEqual(notice.Continuation.NextCall.Input, test.wantInput) ||
				!reflect.DeepEqual(notice.Continuation.Handle, test.wantHandle) {
				t.Fatalf("wrong continuation: %+v", notice)
			}
			first := string(request.fields["input"])
			projectExecutionContinuations(&request, catalog, "exec", test.history)
			if string(request.fields["input"]) != first {
				t.Fatal("projection is not idempotent")
			}
		})
	}
}

func TestExecutionContinuationFollowsCellWithoutPollingInnerSession(t *testing.T) {
	catalog := continuationTestCatalog()
	yielded := "Script running with cell ID cell-17\nWall time 0.1 seconds\nOutput:\n"
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"input": mustMarshalJSON([]any{
			continuationTestCall("shell", "a", "sleep 100"),
			continuationTestOutput("a", yielded),
			continuationTestCall("wait", "b", `{"cell_id":"cell-17"}`),
			continuationTestOutput("b", yielded),
			continuationTestCall("wait", "c", `{"cell_id":"cell-17"}`),
			continuationTestOutput("c", "Script failed\nWall time 0.1 seconds\nOutput:\n",
				`{"output":"partial","session_id":42}`, "Script error:\nhost failure"),
		}),
	}}
	projectExecutionContinuations(&request, catalog, "exec", map[string]mekugiHistory{
		"a": {ToolName: "shell", PluginID: builtinToolsPluginID},
	})
	var items []map[string]json.RawMessage
	_ = json.Unmarshal(request.fields["input"], &items)
	for _, index := range []int{1, 3} {
		if strings.Contains(string(items[index]["output"]), `continuation`) {
			t.Fatalf("resumed cell retained continuation: %s", items[index]["output"])
		}
	}
	if !strings.Contains(string(items[5]["output"]), "write_stdin") {
		t.Fatalf("terminal cell lost session provenance: %s", items[5]["output"])
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
					history["b"] = mekugiHistory{UpstreamItem: resume, Script: source, ToolName: codeModeCommentaryHistoryTool}
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
