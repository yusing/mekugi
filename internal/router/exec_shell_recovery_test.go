package router

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecShellRecoveryDetection(t *testing.T) {
	for _, test := range []struct {
		name, source string
		want         bool
	}{
		{"params Bash", "#!params={\"yield_time_ms\":1000}\nprintf '%s' hello\n", true},
		{"Bash selector", "#!/bin/bash\nprintf '%s' hello\n", true},
		{"Python selector", "#!python3\nfrom pathlib import Path\nprint(Path.cwd())\n", true},
		{"template", "#!cmd=printf input | {.}\ncat -\n", true},
		{"stop batch", "#!batch-stop=NEXT\nexit 7\nNEXT\necho later\n", true},
		{"batch", "#!batch=NEXT\n#!params={}\necho first\nNEXT\n#!python3\nprint('second')\n", true},
		{"valid JavaScript", "text(await tools.clock__curr_time({}));", false},
		{"valid hashbang JavaScript", "#!node\ntext('hello');", false},
		{"valid JavaScript with params hashbang", "#!params={}\ntext('hello');", false},
		{"ambiguous hashbang", "#!bash\nx=1", false},
		{"bare Bash", "echo hello", false},
		{"broken JavaScript", "const result = await tools.run(", false},
		{"quoted header", "text('#!params={}\\necho hello');", false},
		{"comment header", "// #!params={}\necho hello", false},
		{"invalid params", "#!params=broken\necho hello", false},
		{"unknown directive", "#!unknown=value\necho hello", false},
		{"invalid template", "#!cmd=cat\necho hello", false},
		{"empty selector", "#!\necho hello", false},
		{"retained reference is valid JavaScript", "#!script=@shell/prior", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := execShellRecovery(test.source); got != test.want {
				t.Fatalf("recovery=%v, want %v", got, test.want)
			}
		})
	}
}

func TestExecShellRecoveryMissingIdentity(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	item := newResponsesItem(map[string]json.RawMessage{
		"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON("exec"),
		"input": mustMarshalJSON("#!bash\nprintf '%s' hello"),
	})
	if _, err := transform.transformOutputItem(&item); err == nil {
		t.Fatal("accepted recovery without a call ID")
	}
	if len(transform.local) != 0 {
		t.Fatal("retained a malformed call")
	}
}

func TestExecShellRecoveryBatchRuntime(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	directory, bin := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "shell"), []byte("#!/bin/sh\ninterpreter=$1\nshift\nexec \"$interpreter\" -c \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	source := "#!batch=NEXT\n#!params=" + string(mustMarshalJSON(map[string]any{"workdir": directory})) +
		"\nprintf before; exit 7\nNEXT\n#!python3\nfrom pathlib import Path\nPath('order').write_text('python')\nprint('middle')\nNEXT\n" +
		"#!params=" + string(mustMarshalJSON(map[string]any{"workdir": directory, "yield_time_ms": 1000})) +
		"\n#!cmd=printf template | {.}\nread value; printf '%s:' \"$value\"; cat order\n"
	item := newResponsesItem(map[string]json.RawMessage{
		"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON("exec"),
		"call_id": mustMarshalJSON("batch-recovery"), "input": mustMarshalJSON(source),
	})
	if _, err := transform.transformOutputItem(&item); err != nil {
		t.Fatal(err)
	}
	history := transform.local["batch-recovery"]
	if history.TranslationError != "" {
		t.Fatal(history.TranslationError)
	}
	payload, ok := strings.CutPrefix(history.carrierInput(), misuseWarningProjection(execShellRecoveryWarning))
	if !ok {
		t.Fatal("missing recovery notice")
	}
	// The warning itself is covered by the complete-carrier delivery test.
	// This host executes the generated shell commands and decodes their result.
	var result struct {
		Results []struct {
			Output   string `json:"output"`
			ExitCode int    `json:"exit_code"`
		} `json:"results"`
	}
	runShellCatJavaScript(t, proxy.registry.NodeExecutable, directory, payload, &result, "")
	if len(result.Results) != 3 || result.Results[0].Output != "before" || result.Results[0].ExitCode != 7 ||
		result.Results[1].Output != "middle\n" || result.Results[1].ExitCode != 0 ||
		result.Results[2].Output != "template:python" || result.Results[2].ExitCode != 0 {
		t.Fatalf("batch execution changed: %+v", result)
	}
}

func TestExecShellRecoveryPollCorrelation(t *testing.T) {
	transform := &mekugiResponseTransform{
		codeModeToolName: "exec",
		visible: map[string]mekugiHistory{
			"recovered": {
				ToolName: "shell", PluginID: builtinToolsPluginID, Script: "#!params={}\nsleep 20",
				UpstreamItem: map[string]json.RawMessage{"name": mustMarshalJSON("exec")},
			},
		},
	}

	transform.prepareShellActivity(mustMarshalJSON([]any{
		map[string]any{"type": "custom_tool_call", "call_id": "recovered", "name": "exec", "input": "#!params={}\nsleep 20"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "recovered", "output": `{"session_id":42,"output":"start"}`},
	}))
	if got := transform.activityShellSessions["42"]; got != "sleep 20" {
		t.Fatalf("lost recovered command: %q", got)
	}
}
func TestExecShellRecoveryDeliveryAndReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			root, _ := prepareActivityTest(t, proxy, "root", "r", "", "/root", nil)
			transform, _ := prepareActivityTest(t, proxy, "child", "c", "r", "/root/worker", nil)
			source := "#!params={\"workdir\":\"/tmp\",\"yield_time_ms\":1000,\"max_output_tokens\":12000}\nprintf '%s' hello\n"
			upstream := map[string]json.RawMessage{
				"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON("exec"),
				"id": mustMarshalJSON("item-recovery"), "call_id": mustMarshalJSON("call-recovery"),
				"input": mustMarshalJSON(source), "status": mustMarshalJSON("completed"),
				"future": mustMarshalJSON("preserved"),
			}
			var carrier map[string]json.RawMessage
			if stream {
				added := map[string]any{"type": "custom_tool_call", "name": "exec", "id": "item-recovery", "call_id": "call-recovery", "input": ""}
				events := []map[string]any{
					{"type": "response.output_item.added", "item": added},
					{"type": "response.custom_tool_call_input.delta", "item_id": "item-recovery", "delta": source},
					{"type": "response.custom_tool_call_input.done", "item_id": "item-recovery", "call_id": "call-recovery", "input": source},
					{"type": "response.output_item.done", "item": upstream},
					{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{upstream}}},
				}
				for _, event := range events {
					output, err := transform.TransformSSE(mustMarshalJSON(event))
					if err != nil {
						t.Fatal(err)
					}
					for _, raw := range output {
						var envelope struct {
							Type  string                     `json:"type"`
							Input string                     `json:"input"`
							Item  map[string]json.RawMessage `json:"item"`
						}
						if err := json.Unmarshal(raw, &envelope); err != nil {
							t.Fatal(err)
						}
						if envelope.Type == "response.custom_tool_call_input.done" && !strings.Contains(envelope.Input, "exec-shell-recovered") {
							t.Fatal("input.done did not carry recovery")
						}
						if envelope.Type == "response.output_item.done" && jsonString(envelope.Item, "call_id") == "call-recovery" {
							carrier = envelope.Item
						}
					}
				}
			} else {
				output, err := transform.TransformJSON(mustMarshalJSON(map[string]any{"status": "completed", "output": []any{upstream}}))
				if err != nil {
					t.Fatal(err)
				}
				var response struct{ Output []map[string]json.RawMessage }
				if err := json.Unmarshal(output, &response); err != nil {
					t.Fatal(err)
				}
				for _, item := range response.Output {
					if jsonString(item, "call_id") == "call-recovery" {
						carrier = item
					}
				}
			}
			program := jsonString(carrier, "input")
			if strings.Count(program, "exec-shell-recovered") != 1 {
				t.Fatalf("missing or duplicated recovery warning: %s", program)
			}
			// Execute the complete carrier with a host that records native dispatch.
			// The recovered command and all execution params must arrive exactly once.
			host := `const calls=[], output=[]; globalThis.text=x=>output.push(x); globalThis.tools={exec_command:async x=>{calls.push(x);return {output:"hello",exit_code:0};}};` + "\n"
			raw, err := exec.CommandContext(t.Context(), "bun", "--eval", host+program+"\nconsole.log(JSON.stringify({calls,output}));").CombinedOutput()
			if err != nil {
				t.Fatalf("carrier execution: %v: %s", err, raw)
			}
			var result struct {
				Calls  []map[string]json.RawMessage
				Output []json.RawMessage
			}
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Calls) != 1 || jsonString(result.Calls[0], "workdir") != "/tmp" ||
				string(result.Calls[0]["yield_time_ms"]) != "1000" || string(result.Calls[0]["max_output_tokens"]) != "12000" ||
				!strings.Contains(jsonString(result.Calls[0], "cmd"), "printf") {
				t.Fatalf("changed dispatch: %s", raw)
			}
			if len(result.Output) != 2 || !bytes.Contains(result.Output[0], []byte(execShellRecoveryWarning)) ||
				!bytes.Contains(result.Output[1], []byte("hello")) {
				t.Fatalf("lost warning or native result: %s", raw)
			}
			history, ok := proxy.history(transform.historySessionID, "call-recovery")
			if !ok || history.ToolName != "shell" || jsonString(history.UpstreamItem, "name") != "exec" || history.ReplayCarrier {
				t.Fatalf("incorrect recovery history: %+v", history)
			}
			replay, err := parseResponsesRequest(mustMarshalJSON(map[string]any{"input": []any{
				carrier, map[string]any{"type": "custom_tool_call_output", "call_id": "call-recovery", "output": "hello"},
			}}))
			if err != nil {
				t.Fatal(err)
			}
			if err := proxy.reconcileInputPrefix(&replay, transform.historySessionID); err != nil {
				t.Fatal(err)
			}
			var restored []map[string]json.RawMessage
			if err := json.Unmarshal(replay.fields["input"], &restored); err != nil {
				t.Fatal(err)
			}
			if !sameJSONValue(mustMarshalJSON(restored[0]), mustMarshalJSON(upstream)) || jsonString(restored[1], "output") != "hello" {
				t.Fatalf("replay changed original call/result: %s", replay.fields["input"])
			}
			activity, err := root.TransformJSON([]byte(`{"status":"completed","output":[]}`))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(activity, []byte("Run JavaScript")) || !bytes.Contains(activity, []byte("```bash")) {
				t.Fatalf("wrong recovered display: %s", activity)
			}
		})
	}
}

func TestExecShellRecoveryUsesShellValidation(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
	source := "#!params={\"cmd\":\"touch must-not-run\"}\nprintf '%s' hello\n"
	item := newResponsesItem(map[string]json.RawMessage{
		"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON("exec"),
		"call_id": mustMarshalJSON("invalid-params"), "input": mustMarshalJSON(source),
	})
	if _, err := transform.transformOutputItem(&item); err != nil {
		t.Fatal(err)
	}
	history := transform.local["invalid-params"]
	if history.TranslationError == "" || strings.Contains(history.carrierInput(), "tools.exec_command") {
		t.Fatalf("invalid params escaped validation: %+v", history)
	}
}

func TestExecShellRecoveryPreservesCodeMode(t *testing.T) {
	for _, source := range []string{"text('hello');", "#!node\ntext('hello');", "#!params={}\ntext('hello');", "echo hello", "const r = await tools.run("} {
		transform, _, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
		item := newResponsesItem(map[string]json.RawMessage{
			"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON("exec"),
			"call_id": mustMarshalJSON("unchanged"), "input": mustMarshalJSON(source),
		})
		if _, err := transform.transformOutputItem(&item); err != nil {
			t.Fatal(err)
		}
		if got := jsonString(item.fields, "input"); got != source {
			t.Fatalf("changed non-recovery source: %q => %q", source, got)
		}
	}
}

func TestExecShellRecoveryRequiresBuiltinShell(t *testing.T) {
	for _, configured := range []bool{false, true} {
		transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, new(int)))
		proxy.registry = &toolRegistry{byName: maps.Clone(proxy.registry.byName)}
		if configured {
			contribution := proxy.registry.byName["shell"]
			contribution.PluginID = "configured"
			proxy.registry.byName["shell"] = contribution
		} else {
			delete(proxy.registry.byName, "shell")
		}
		source := "#!bash\nprintf '%s' hello"
		item := newResponsesItem(map[string]json.RawMessage{
			"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON("exec"),
			"call_id": mustMarshalJSON("no-builtin"), "input": mustMarshalJSON(source),
		})
		if _, err := transform.transformOutputItem(&item); err != nil {
			t.Fatal(err)
		}
		if got := jsonString(item.fields, "input"); got != source {
			t.Fatalf("recovered without built-in shell: %q", got)
		}
	}
}

func TestExecShellRecoveryDoesNotCorrelateReverseRecoveryOutput(t *testing.T) {
	transform := &mekugiResponseTransform{
		codeModeToolName: "exec",
		visible: map[string]mekugiHistory{
			"reverse": {
				ToolName: "shell", PluginID: builtinToolsPluginID, Script: `text('{"session_id":42}')`,
				ReplayCarrier: true,
				UpstreamItem:  map[string]json.RawMessage{"name": mustMarshalJSON("shell")},
			},
		},
	}
	transform.prepareShellActivity(mustMarshalJSON([]any{
		map[string]any{"type": "custom_tool_call", "call_id": "reverse", "name": "exec", "input": `text('{"session_id":42}')`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "reverse", "output": []any{
			map[string]any{"type": "text", "text": shellCodeModeRecoveryWarning},
			map[string]any{"type": "text", "text": `{"session_id":42}`},
		}},
	}))
	if len(transform.activityShellSessions) != 0 {
		t.Fatal("inferred a shell session from recovered JavaScript output")
	}
}
