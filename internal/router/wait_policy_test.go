package router

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"os/exec"
	"strings"
	"testing"
)

func TestWaitPolicyArguments(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
	}{
		{"wait", `{"cell_id":"x"}`, `{"cell_id":"x","yield_time_ms":300000}`},
		{"wait", `{"cell_id":"x","yield_time_ms":1}`, `{"cell_id":"x","yield_time_ms":300000}`},
		{"wait", `{"cell_id":"x","yield_time_ms":600000}`, ""},
		{"wait", `{"cell_id":"x","terminate":true,"yield_time_ms":1}`, ""},
		{"wait", `{"cell_id":"x","yield_time_ms":null}`, ""},
		{"wait", `{"cell_id":"x","yield_time_ms":"1"}`, ""},
		{"wait", `{"cell_id":"x","yield_time_ms":-1}`, ""},
		{"write_stdin", `{"session_id":42,"chars":""}`, `{"chars":"","session_id":42,"yield_time_ms":300000}`},
		{"write_stdin", `{"session_id":42}`, `{"session_id":42,"yield_time_ms":300000}`},
		{"write_stdin", `{"session_id":42,"chars":"y","yield_time_ms":1}`, ""},
		{"write_stdin", `{"session_id":42,"chars":"\u0003","yield_time_ms":1}`, ""},
		{"write_stdin", `{"session_id":42,"chars":null}`, ""},
		{"wait", `not JSON`, ""},
	} {
		want := test.want
		if want == "" {
			want = test.input
		}
		if got := (waitPolicy{"yield_time_ms", 300000}).rewrite(test.name, test.input); got != want {
			t.Errorf("%s %s: got %s, want %s", test.name, test.input, got, want)
		}
	}
}

func TestWaitPolicyCatalog(t *testing.T) {
	catalog := decodeResponsesToolCatalog(map[string]jsonv1.RawMessage{
		"tools": mustMarshalJSON([]any{
			map[string]any{"type": "function", "name": "wait", "parameters": map[string]any{"properties": map[string]any{
				"cell_id":       map[string]any{"type": "string"},
				"yield_time_ms": map[string]any{"type": "integer", "maximum": jsonv1.Number("120000.5")},
			}}},
			map[string]any{"type": "namespace", "name": "mekugi_collaboration", "tools": []any{
				map[string]any{"type": "function", "name": "wait_agent", "parameters": map[string]any{"properties": map[string]any{
					"timeout_ms": map[string]any{"type": "number"},
				}}},
			}},
		}),
	})
	policies := collectWaitPolicies(catalog, "exec")
	if policies.direct[functionToolKey("", "wait")].floor != 120000 ||
		policies.direct[functionToolKey("mekugi_collaboration", "wait_agent")].floor != 300000 {
		t.Fatalf("policies = %+v", policies)
	}
}

func TestWaitPolicyJSONAndReplay(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	transform.waitPolicies.direct = map[string]waitPolicy{functionToolKey("functions", "wait"): {"yield_time_ms", 300000}}
	original := `{"cell_id":"cell-1","yield_time_ms":1}`
	item := map[string]any{"type": "function_call", "id": "item-wait", "call_id": "call-wait", "namespace": "functions", "name": "wait", "arguments": original}
	result, err := transform.TransformJSON(mustMarshalJSON(map[string]any{"status": "completed", "output": []any{item}}))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []map[string]jsonv1.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(result, &response); err != nil || len(response.Output) != 1 {
		t.Fatalf("response %s: %v", result, err)
	}
	if !strings.Contains(jsonString(response.Output[0], "arguments"), `"yield_time_ms":300000`) {
		t.Fatalf("not rewritten: %s", result)
	}
	request := &parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustMarshalJSON(response.Output)}}
	if err := proxy.reconcileInputPrefix(request, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	var restored []map[string]jsonv1.RawMessage
	if err := json.Unmarshal(request.fields["input"], &restored); err != nil || jsonString(restored[0], "arguments") != original {
		t.Fatalf("replay = %s, %v", request.fields["input"], err)
	}
}

func TestWaitPolicyStreamDurableBeforeTerminal(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	directory := t.TempDir()
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	transform.waitPolicies.direct = map[string]waitPolicy{functionToolKey("functions", "wait"): {"yield_time_ms", 300000}}
	item := map[string]any{"type": "function_call", "id": "item-wait", "call_id": "call-wait", "namespace": "functions", "name": "wait", "arguments": ""}
	if events, err := transform.TransformSSE(mustMarshalJSON(map[string]any{"type": "response.output_item.added", "item": item})); err != nil || len(events) != 0 {
		t.Fatalf("added = %s, %v", events, err)
	}
	original := `{"cell_id":"cell-1","yield_time_ms":1}`
	for _, kind := range []string{"response.function_call_arguments.delta", "response.function_call_arguments.done"} {
		events, err := transform.TransformSSE(mustMarshalJSON(map[string]any{"type": kind, "item_id": "item-wait", "arguments": original, "delta": original}))
		if err != nil || len(events) != 1 || bytes.Contains(events[0], []byte("cell-1")) {
			t.Fatalf("partial input exposed: %s, %v", events, err)
		}
	}
	item["arguments"] = original
	events, err := transform.TransformSSE(mustMarshalJSON(map[string]any{"type": "response.output_item.done", "item": item}))
	if err != nil || len(events) != 3 {
		t.Fatalf("done = %s, %v", events, err)
	}
	var done struct {
		Item map[string]jsonv1.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(events[2], &done); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonString(done.Item, "arguments"), `"yield_time_ms":300000`) {
		t.Fatalf("not rewritten: %s", events)
	}
	reopened, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &mekugiProxy{replayStore: reopened}
	request := &parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustMarshalJSON([]any{done.Item})}}
	if _, err := restarted.reconcileVisibleInput(t.Context(), request, transform.directory, "forked-route"); err != nil {
		t.Fatal(err)
	}
	var restored []map[string]jsonv1.RawMessage
	if err := json.Unmarshal(request.fields["input"], &restored); err != nil || jsonString(restored[0], "arguments") != original {
		t.Fatalf("restart replay = %s, %v", request.fields["input"], err)
	}
}

func TestWaitPolicyCodeModeRuntime(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	source := `
const values = [{session_id:42,chars:"",yield_time_ms:1}, {session_id:42,chars:"y",yield_time_ms:1}, {session_id:42,chars:"",yield_time_ms:600000}];
for (const value of values) text(await tools.write_stdin(value));
text(await tools["write_stdin"]({session_id:42}));
if (values[0].yield_time_ms !== 1) throw new Error("mutated caller argument");
`
	transformed, changed := rewriteCodeModeWaits(source, map[string]waitPolicy{"write_stdin": {"yield_time_ms", 300000}})
	if !changed {
		t.Fatal("not rewritten")
	}
	program := `const tools = {async write_stdin(args) { if (this !== tools) throw new Error('wrong receiver'); return args.yield_time_ms; }}; const text = value => console.log(value); (async()=>{` + transformed + `})().catch(e=>{console.error(e);process.exitCode=1});`
	result, err := exec.CommandContext(t.Context(), node, "-e", program).CombinedOutput()
	if err != nil || string(result) != "300000\n1\n600000\n300000\n" {
		t.Fatalf("runtime = %s, %v", result, err)
	}
	for _, unchanged := range []string{
		`text("await tools.write_stdin({})");`,
		`// tools.write_stdin({})`,
		`const tools = {write_stdin: x => x}; tools.write_stdin({});`,
		`const write_stdin = "other"; await tools[write_stdin]({yield_time_ms:1});`,
		`(tools => tools.write_stdin({yield_time_ms:1}))({write_stdin:a=>a});`,
		`const {tools} = local; tools.write_stdin({});`,
		`const {x:tools} = local; tools.write_stdin({});`,
		`((tools = local) => tools.write_stdin({}))();`,
		`((...tools) => tools.write_stdin({}))();`,
		`try {} catch (tools) { tools.write_stdin({}); }`,
		`function tools() {} tools.write_stdin({});`,
		`for (const tools of [{write_stdin:a=>a}]) tools.write_stdin({});`,
		`for ({tools} of locals) tools.write_stdin({});`,
		`tools = local; tools.write_stdin({});`,
		`await tools.write_stdin(`,
	} {
		if got, changed := rewriteCodeModeWaits(unchanged, map[string]waitPolicy{"write_stdin": {"yield_time_ms", 300000}}); changed || got != unchanged {
			t.Fatalf("unrelated source changed: %s", got)
		}
	}
}

func TestWaitPolicyNestedPreparedRequestAndReplay(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	description := testCodeModeDescription + "\n### `write_stdin`\ndeclare const tools: { write_stdin(args: { session_id: number; chars?: string; yield_time_ms?: number }): Promise<unknown>; };\n"
	workspace := t.TempDir()
	request, err := parseResponsesRequest(mustMarshalJSON(map[string]any{
		"model": "gpt-test", "tools": []any{}, "tool_choice": "auto",
		"input": []any{testCodeModeAdditionalTools(description)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	transform, err := proxy.prepareRequest(t.Context(), &request, "session-wait", "thread-wait",
		codexTurnMetadata{RequestKind: "turn", Directories: map[string]jsonv1.RawMessage{workspace: nil}}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer transform.Close()
	original := `text(await tools.write_stdin({session_id:42,chars:"",yield_time_ms:1}));`
	result, err := transform.TransformJSON(mustMarshalJSON(map[string]any{
		"status": "completed", "output": []any{continuationTestCall("exec", "nested-wait", original)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []map[string]jsonv1.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(result, &response); err != nil || len(response.Output) != 1 {
		t.Fatalf("response = %s, %v", result, err)
	}
	carrier := jsonString(response.Output[0], "input")
	if carrier == original || !strings.Contains(carrier, "300000") {
		t.Fatalf("not rewritten: %s", carrier)
	}
	// A second completion event reuses the retained carrier, without wrapping it again.
	again, err := transform.TransformJSON(mustMarshalJSON(map[string]any{
		"status": "completed", "output": []any{continuationTestCall("exec", "nested-wait", original)},
	}))
	if err != nil || !bytes.Equal(result, again) {
		t.Fatalf("repeated completion = %s, %v", again, err)
	}
	replay := &parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustMarshalJSON(response.Output)}}
	if err := proxy.reconcileInputPrefix(replay, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	var restored []map[string]jsonv1.RawMessage
	if err := json.Unmarshal(replay.fields["input"], &restored); err != nil || jsonString(restored[0], "input") != original {
		t.Fatalf("nested replay = %s, %v", replay.fields["input"], err)
	}
}
