package router

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/shell"
)

func TestCodeModeCommentaryLowersRuntimeExpressionAndPreservesOriginal(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	overrides := journalCodeModeRuntime(t, transform)
	source := "for (let i = 1; i <= 2; i++) {\n" +
		"  await journal({op: 'add', text: `Running ${i}/2`});\n" +
		"}\n" +
		"text('await journal(ignored)');"
	item := map[string]json.RawMessage{
		"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON(transform.codeModeToolName),
		"call_id": mustMarshalJSON("call-code"), "id": mustMarshalJSON("item-code"), "input": mustMarshalJSON(source),
	}
	view := newResponsesItem(item)
	changed, err := transform.transformOutputItem(&view)
	if err != nil || !changed {
		t.Fatalf("changed = %v, error %v", changed, err)
	}
	lowered := jsonString(item, "input")
	for _, required := range []string{"await tools.exec_command", "encodeURIComponent(JSON.stringify(mutation))", "text: `Running ${i}/2`", commentaryOnceArgument} {
		if !strings.Contains(lowered, required) {
			t.Fatalf("lowered input missing %q: %s", required, lowered)
		}
	}
	if strings.Count(lowered, commentaryOnceArgument) != 1 || !strings.Contains(lowered, "text('await journal(ignored)')") {
		t.Fatalf("lowered input = %s", lowered)
	}
	var result string
	runShellCatJavaScript(t, proxy.registry.NodeExecutable, transform.directory,
		strings.Replace(lowered, "text('await journal(ignored)');", `text(JSON.stringify("finished"));`, 1), &result, overrides)
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID)
	if err != nil || len(items) != 2 || items[0].Text != "Running 1/2" || items[1].Text != "Running 2/2" {
		t.Fatalf("runtime journal = %+v, error = %v", items, err)
	}
	history := transform.local["call-code"]
	if history.script != source || jsonString(history.upstreamItem, "input") != source || history.carrierPayload != lowered {
		t.Fatalf("history = %+v", history)
	}
	repeated := maps.Clone(item)
	repeated["input"] = mustMarshalJSON(source)
	repeatedView := newResponsesItem(repeated)
	if changed, err := transform.transformOutputItem(&repeatedView); err != nil || !changed || jsonString(repeated, "input") != lowered {
		t.Fatalf("repeated lower = changed %v, error %v, input %s", changed, err, repeated["input"])
	}
}

func TestCodeModeJournalUsesOneRouteAndRejectsExhaustion(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	lowered, changed, err := transform.lowerCodeModeCommentary(
		"call-code", "await journal({op: 'add', text: 'first'});\nawait journal({op: 'add', text: 'second'});",
	)
	if err != nil || !changed || strings.Count(lowered, commentaryOnceArgument) != 2 {
		t.Fatalf("lowered = %q, changed = %v, error %v", lowered, changed, err)
	}
	proxy.commentary.mu.Lock()
	routes := len(proxy.commentary.routes)
	proxy.commentary.mu.Unlock()
	if routes != 1 {
		t.Fatalf("publisher routes = %d", routes)
	}

	for index := routes; index < maxCommentaryRoutes; index++ {
		if proxy.commentary.subscribe("session", "call", "") == "" {
			t.Fatalf("route %d was rejected early", index)
		}
	}
	lowered, changed, err = transform.lowerCodeModeCommentary("call-fallback", "await journal(sideEffect());")
	if err == nil || changed || lowered != "" {
		t.Fatalf("fallback = %q, changed = %v, error %v", lowered, changed, err)
	}
}

func TestCodeModeJournalSupportsNestedExpressions(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	overrides := journalCodeModeRuntime(t, transform)
	lowered, changed, err := transform.lowerCodeModeCommentary(
		"call-nested", `await journal({op: "edit", id: await journal({op: "add", text: "inner"}), text: "outer"});`,
	)
	if err != nil || !changed || strings.Count(lowered, commentaryOnceArgument) != 2 ||
		strings.Count(lowered, "await tools.exec_command") != 2 {
		t.Fatalf("lowered = %q, changed = %v, error = %v", lowered, changed, err)
	}
	var result string
	runShellCatJavaScript(t, proxy.registry.NodeExecutable, transform.directory,
		`text(JSON.stringify(`+strings.TrimSuffix(lowered, ";")+`));`, &result, overrides)
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID)
	if err != nil || result != "j1" || len(items) != 1 || items[0].ID != result || items[0].Text != "outer" {
		t.Fatalf("nested journal result=%q items=%+v error=%v", result, items, err)
	}
}

// Execute the lowered command through the real worker and authenticated publisher;
// only the Codex execution transport is replaced by a local HTTP fixture.
func journalCodeModeRuntime(t *testing.T, transform *mekugiResponseTransform) string {
	t.Helper()
	proxy := transform.proxy
	mux := http.NewServeMux()
	mux.HandleFunc("/publish", proxy.commentary.serveHTTP)
	mux.HandleFunc("/execute", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Cmd string `json:"cmd"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid command", http.StatusBadRequest)
			return
		}
		args, err := shell.Fields(request.Cmd, func(string) string { return "" })
		if err != nil || len(args) == 0 || args[0] != "shell" {
			t.Errorf("invalid journal worker command: %v", err)
			http.Error(w, "invalid command", http.StatusBadRequest)
			return
		}
		var stdout, stderr bytes.Buffer
		handled, code := RunToolPluginWorker(r.Context(), proxy.registry.shellRuntime, args[1:], nil, &stdout, &stderr)
		if !handled {
			t.Error("journal worker command was not handled")
			code = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"output": stdout.String() + stderr.String(), "exit_code": code})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	proxy.commentaryEndpoint = server.URL + "/publish"
	return `tools.exec_command = async args => {
const response = await fetch(` + strconv.Quote(server.URL+"/execute") + `, {method: "POST", body: JSON.stringify(args)});
if (!response.ok) throw new Error("worker transport failed");
return response.json();
};`
}

func TestCodeModeCommentaryLowersAuthoritativeStreamingInput(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	source := `await journal({op: "add", text: "Working"});`
	added := mustTestJSON(t, map[string]any{
		"type": "response.output_item.added", "item": map[string]any{
			"type": "custom_tool_call", "id": "item-code", "call_id": "call-code",
			"name": transform.codeModeToolName, "input": "",
		},
	})
	if events, err := transform.TransformSSE(added); err != nil || len(events) != 1 {
		t.Fatalf("added events = %q, error = %v", events, err)
	}
	inputDone := mustTestJSON(t, map[string]any{
		"type": "response.custom_tool_call_input.done", "item_id": "item-code",
		"call_id": "call-code", "input": source,
	})
	events, err := transform.TransformSSE(inputDone)
	if err != nil || len(events) != 1 {
		t.Fatalf("input events = %q, error = %v", events, err)
	}
	var completedInput struct {
		Input string `json:"input"`
	}
	if json.Unmarshal(events[0], &completedInput) != nil || completedInput.Input == source ||
		!strings.Contains(completedInput.Input, commentaryOnceArgument) {
		t.Fatalf("completed input = %q", events[0])
	}
	itemDone := mustTestJSON(t, map[string]any{
		"type": "response.output_item.done", "item": map[string]any{
			"type": "custom_tool_call", "id": "item-code", "call_id": "call-code",
			"name": transform.codeModeToolName, "input": source, "status": "completed",
		},
	})
	events, err = transform.TransformSSE(itemDone)
	if err != nil || len(events) != 1 {
		t.Fatalf("item events = %q, error = %v", events, err)
	}
	var completedItem struct {
		Item map[string]json.RawMessage `json:"item"`
	}
	if json.Unmarshal(events[0], &completedItem) != nil ||
		jsonString(completedItem.Item, "input") != completedInput.Input {
		t.Fatalf("completed item = %q, input = %q", events[0], completedInput.Input)
	}
	if jsonString(transform.local["call-code"].upstreamItem, "status") != "completed" {
		t.Fatalf("retained item = %s", mustMarshalJSON(transform.local["call-code"].upstreamItem))
	}
}

func TestCodeModeNativeWarningPreservesDurableStreamingInput(t *testing.T) {
	for _, source := range []string{
		"const result = await tools.exec_command({cmd:\"true\",max_output_tokens:1000});\ntext(result);\n",
		"await commentary('Working');\ntext(await tools.exec_command({cmd:'true'}));",
	} {
		t.Run(source, func(t *testing.T) {
			transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
			proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
			directory := t.TempDir()
			store, err := openMekugiReplayStore(directory)
			if err != nil {
				t.Fatal(err)
			}
			proxy.replayStore = store
			item := map[string]any{
				"type": "custom_tool_call", "id": "item-warning", "call_id": "call-warning",
				"name": transform.codeModeToolName, "input": "", "status": "in_progress",
			}
			if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
				"type": "response.output_item.added", "item": item,
			})); err != nil {
				t.Fatal(err)
			}
			if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
				"type": "response.custom_tool_call_input.done", "item_id": "item-warning",
				"call_id": "call-warning", "input": source,
			})); err != nil {
				t.Fatal(err)
			}
			item["input"], item["status"] = source, "completed"
			if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
				"type": "response.output_item.done", "item": item,
			})); err != nil {
				t.Fatalf("complete streamed call: %v", err)
			}
			store, err = openMekugiReplayStore(directory)
			if err != nil {
				t.Fatal(err)
			}
			history, found, err := store.lookup(t.Context(), transform.directory, "call-warning")
			if err != nil || !found {
				t.Fatalf("restart lookup: found=%v, error=%v", found, err)
			}
			if history.script != source || jsonString(history.upstreamItem, "input") != source {
				t.Fatal("durable replay did not preserve original provider input")
			}
			if jsonString(history.upstreamItem, "status") != "completed" {
				t.Fatal("durable replay did not retain completion")
			}
			if !strings.Contains(history.carrierPayload, nativeExecCommandWarning) {
				t.Fatal("executor carrier lost the native-command warning")
			}
		})
	}
}

func TestCodeModeWithoutExplicitCommentaryPreservesOutput(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	item := map[string]json.RawMessage{
		"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON(transform.codeModeToolName),
		"call_id": mustMarshalJSON("call-default"), "id": mustMarshalJSON("item-default"),
		"input": mustMarshalJSON("text('done');"),
	}
	output, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{item}}))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 1 || jsonString(response.Output[0], "input") != "text('done');" {
		t.Fatalf("operation output changed: %s", output)
	}
}

func TestCodeModeUnparseableInputPassesThrough(t *testing.T) {
	for _, source := range []string{
		`text("unterminated);`,
		`await journal("Working"); text(`,
		`const value: number = 1; text(value);`,
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(source+"/streaming="+strconv.FormatBool(streaming), func(t *testing.T) {
				transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
				proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
				item := map[string]any{
					"type": "custom_tool_call", "name": transform.codeModeToolName,
					"call_id": "call-invalid", "id": "item-invalid", "input": source,
					"status": "completed",
				}
				response := map[string]any{"status": "completed", "output": []any{item}}
				if streaming {
					added := maps.Clone(item)
					added["input"] = ""
					added["status"] = "in_progress"
					for _, event := range []map[string]any{
						{"type": "response.output_item.added", "item": added},
						{"type": "response.custom_tool_call_input.delta", "item_id": "item-invalid", "delta": source},
						{"type": "response.custom_tool_call_input.done", "item_id": "item-invalid", "call_id": "call-invalid", "input": source},
						{"type": "response.output_item.done", "item": item},
						{"type": "response.completed", "response": response},
					} {
						payload := mustTestJSON(t, event)
						events, err := transform.TransformSSE(payload)
						if err != nil || len(events) != 1 || !bytes.Equal(events[0], payload) {
							t.Fatalf("event %s: output = %s, error = %v", event["type"], events, err)
						}
					}
				} else {
					output, err := transform.TransformJSON(mustTestJSON(t, response))
					if err != nil {
						t.Fatal(err)
					}
					var decoded struct {
						Output []map[string]json.RawMessage `json:"output"`
					}
					if err := json.Unmarshal(output, &decoded); err != nil {
						t.Fatal(err)
					}
					if len(decoded.Output) != 1 || jsonString(decoded.Output[0], "input") != source {
						t.Fatalf("input changed: %s", output)
					}
				}
				history := transform.local["call-invalid"]
				if history.script != source || history.carrierPayload != source || jsonString(history.upstreamItem, "input") != source {
					t.Fatal("replay did not retain the exact program")
				}
				if len(transform.commentarySubscriptions) != 0 {
					t.Fatal("unparseable program created a commentary subscription")
				}
			})
		}
	}
}

func TestCodeModeNativeExecStreamingRetainsOriginalInput(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	source := `text(await tools.exec_command({cmd: "true"}));`
	item := map[string]any{
		"type": "custom_tool_call", "id": "item-native", "call_id": "call-native",
		"name": transform.codeModeToolName, "input": "",
	}
	events := []map[string]any{
		{"type": "response.output_item.added", "item": item},
		{"type": "response.custom_tool_call_input.done", "item_id": "item-native",
			"call_id": "call-native", "input": source},
	}
	for _, event := range events {
		if _, err := transform.TransformSSE(mustTestJSON(t, event)); err != nil {
			t.Fatal(err)
		}
	}
	history, found, err := store.lookup(t.Context(), transform.directory, "call-native")
	if err != nil || !found {
		t.Fatalf("lookup: found %v, error %v", found, err)
	}
	if jsonString(history.upstreamItem, "input") != source {
		t.Fatalf("durable upstream input was rewritten: %s", history.upstreamItem["input"])
	}
	if history.carrierPayload == source {
		t.Fatal("native-exec warning was not added to the carrier")
	}
	item["input"] = source
	item["status"] = "completed"
	for _, event := range []map[string]any{
		{"type": "response.output_item.done", "item": item},
		{"type": "response.completed", "response": map[string]any{
			"status": "completed", "output": []any{item},
		}},
	} {
		if _, err := transform.TransformSSE(mustTestJSON(t, event)); err != nil {
			t.Fatalf("completion rejected unchanged source: %v", err)
		}
	}
}

func TestCodeModeJournalArrayAndYieldedResult(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	source := `text(JSON.stringify(await journal([{op:"add",text:"first"},{op:"add",text:"second"}])));`
	lowered, _, err := transform.lowerCodeModeCommentary("yielded", source)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	runShellCatJavaScript(t, proxy.registry.NodeExecutable, transform.directory, lowered, &ids, `
let calls = 0;
tools.exec_command = async () => {
  if (++calls !== 1) throw new Error("publication replayed");
  return {session_id:42,output:'{"ok":true,'};
};
tools.write_stdin = async args => {
  if (args.session_id !== 42 || args.chars !== "") throw new Error("wrong continuation");
  return {exit_code:0,output:'"items":["j1","j2"]}'};
};`)
	if len(ids) != 2 || ids[0] != "j1" || ids[1] != "j2" {
		t.Fatalf("array IDs = %v", ids)
	}
}

func TestCodeModeJournalPublicationFailureThrows(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	lowered, _, err := transform.lowerCodeModeCommentary("failed", `await journal({op:"add",text:"failed"});`)
	if err != nil {
		t.Fatal(err)
	}
	var result string
	runShellCatJavaScript(t, proxy.registry.NodeExecutable, transform.directory,
		`let result="unexpected success"; try {`+lowered+`} catch (error) {result=error.message;} text(JSON.stringify(result));`, &result,
		`tools.exec_command = async () => ({exit_code:1,output:"rejected"});`)
	if result != "journal publication failed" {
		t.Fatalf("publication error = %q", result)
	}
}
