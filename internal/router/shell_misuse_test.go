package router

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestShellTypeScriptMisuse(t *testing.T) {
	for _, test := range []struct {
		name, interpreter, body string
		want                    bool
	}{
		{"text helper", "bash", `text("I cannot send collaboration tool from exec")`, true},
		{"tool discovery", "bash", `const hits = ALL_TOOLS.filter(x => /send_message|collaboration/.test(x.name+" "+x.description)); text(hits);`, true},
		{"typed script", "/usr/bin/bash", `const count: number = 1; console.log(count);`, true},
		{"leading comment", "bash", "// comment\ntext('ok');", true},
		{"nested tool", "bash", `const r = await tools.exec_command({cmd: "touch should-not-exist"}); text(r);`, true},
		{"valid in both", "bash", "echo", false},
		{"assignment", "bash", "x=1", false},
		{"function", "bash", "text() { printf '%s' ok; }; text", false},
		{"quoted JavaScript", "bash", `printf '%s' 'text("hello");'`, false},
		{"heredoc", "bash", "cat <<'EOF'\nconst count: number = 1;\nEOF\n", false},
		{"broken in both", "bash", "if then (", false},
		{"explicit node", "node", `console.log("ok");`, false},
		{"explicit bun", "bun", `const n: number = 1;`, false},
		{"explicit POSIX", "sh", `text("ok");`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			contribution := toolContribution{PluginID: builtinToolsPluginID, Name: "shell"}
			arguments := []string{test.interpreter, test.body}
			if got := shellTypeScriptMisuse(contribution, arguments); got != test.want {
				t.Fatalf("misuse = %v, want %v", got, test.want)
			}
			contribution.PluginID = "configured"
			if shellTypeScriptMisuse(contribution, arguments) {
				t.Fatal("configured plugin was intercepted")
			}
		})
	}
}

func TestShellCodeModeRecoverySyntaxTree(t *testing.T) {
	for _, test := range []struct {
		name, input string
		want        bool
	}{
		{"original text helper", `text("I cannot send collaboration tool from exec")`, true},
		{"original discovery", `const hits = ALL_TOOLS.filter(x => /send_message|collaboration/.test(x.name+" "+x.description)); text(hits);`, true},
		{"comments and formatting", "// inspect tools\nconst r = await tools /* comment */ . exec ({});\ntext\n(r);", true},
		{"projection first", `text("starting"); const r = await tools.exec({}); text(r);`, true},
		{"nested calls", `text(await tools.exec({}));`, true},
		{"parallel calls", `const results = await Promise.all([tools.one({}), tools.two({})]); text(results);`, true},
		{"computed method", `const r = await tools["exec"]({}); console.log(r);`, true},
		{"no await", `tools.exec({}).then(text);`, true},
		{"no projection", `await tools.exec({});`, true},
		{"ordinary script", `console.log("hello");`, false},
		{"quoted code", `const s = 'await tools.exec({}); text(r); ALL_TOOLS'; console.log(s);`, false},
		{"comment only", "// await tools.exec({}); text(r); ALL_TOOLS\nconsole.log('ok');", false},
		{"regex literal", `const pattern = /text(foo)|ALL_TOOLS/; console.log(pattern);`, false},
		{"template literal", "const s = `text(r); ALL_TOOLS`; console.log(s);", false},
		{"property names", `const value = {ALL_TOOLS: [], text: 1}; console.log(value.text);`, false},
		{"local helper", `const text = value => console.log(value); text("hello");`, false},
		{"local tools", `const tools = {run() {}}; tools.run();`, false},
		{"local catalog", `const ALL_TOOLS = []; console.log(ALL_TOOLS);`, false},
		{"function binding", `function text(x) { console.log(x); } text(1);`, false},
		{"parameter binding", `function run(text) { text(1); } run(console.log);`, false},
		{"arrow binding", `(text => text(1))(console.log);`, false},
		{"destructured binding", `const {text} = console; text(1);`, false},
		{"import binding", `import {text} from "local"; text(1);`, false},
		{"reassigned helper", `text = console.log; text(1);`, false},
		{"loop declaration", `for (const text of [console.log]) { text("ordinary JS"); }`, false},
		{"loop destructuring", `for (const {text} of [{text: console.log}]) { text(1); }`, false},
		{"loop assignment", `for (text of [console.log]) { text(1); }`, false},
		{"loop catalog", `for (const ALL_TOOLS in {first: 1}) { console.log(ALL_TOOLS); }`, false},
		{"loop tools", `for (const tools of [{run() {}}]) { tools.run(); }`, false},
		{"explicit interpreter", "#!node\ntext('hello');", false},
		{"params", "#!params={}\ntext('hello');", false},
		{"Bash function", "text() { echo hi; }; text", false},
		{"TypeScript only", `const count: number = 1; text(count);`, false},
		{"invalid JavaScript", `text(`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			contribution := toolContribution{PluginID: builtinToolsPluginID, Name: "shell"}
			if got := shellCodeModeRecovery(contribution, test.input); got != test.want {
				t.Fatalf("recovery=%v, want %v", got, test.want)
			}
		})
	}
}

func TestShellMisuseRejectionDeliveryAndReplay(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run("native="+strconv.FormatBool(native)+"/stream="+strconv.FormatBool(streaming), func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				var transform *mekugiResponseTransform
				if native {
					transform, _ = newNativeMekugiTestTransformWithProxy(t, proxy)
				} else {
					transform, _, _, _ = newMekugiTestTransformWithProxy(t, proxy)
				}
				// A nested executor call may not run during rejection.
				input := "#!/bin/bash\n#!params={\"yield_time_ms\":1000}\nconst r = await tools.exec_command({cmd: 'touch script-ran'}); text(r);"
				item := map[string]any{"type": "custom_tool_call", "name": "shell", "id": "item-shell", "call_id": "call-shell", "input": input, "status": "completed"}
				var carrier map[string]json.RawMessage
				if streaming {
					events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": item}))
					if err != nil {
						t.Fatal(err)
					}
					for _, event := range events {
						var envelope struct {
							Item map[string]json.RawMessage `json:"item"`
						}
						if err := json.Unmarshal(event, &envelope); err != nil {
							t.Fatal(err)
						}
						if jsonString(envelope.Item, "call_id") == "call-shell" {
							carrier = envelope.Item
						}
					}
				} else {
					visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{item}}))
					if err != nil {
						t.Fatal(err)
					}
					var response struct {
						Output []map[string]json.RawMessage `json:"output"`
					}
					if err := json.Unmarshal(visible, &response); err != nil {
						t.Fatal(err)
					}
					for _, output := range response.Output {
						if jsonString(output, "call_id") == "call-shell" {
							carrier = output
						}
					}
				}
				if carrier == nil {
					t.Fatal("missing rejection carrier")
				}
				if native {
					var arguments map[string]json.RawMessage
					if err := json.Unmarshal([]byte(jsonString(carrier, "arguments")), &arguments); err != nil {
						t.Fatal(err)
					}
					directory := t.TempDir()
					command := exec.CommandContext(t.Context(), "bash", "-c", jsonString(arguments, "cmd"))
					command.Dir = directory
					output, err := command.CombinedOutput()
					if err != nil || string(output) != shellTypeScriptDiagnostic {
						t.Fatalf("diagnostic delivery: %q, %v", output, err)
					}
					for _, name := range []string{"script-ran"} {
						if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
							t.Fatalf("unexpected execution: %s", name)
						}
					}
				} else if got := jsonString(carrier, "input"); got != "text("+strconv.Quote(shellTypeScriptDiagnostic)+");" {
					t.Fatalf("rejection executes something other than the diagnostic: %s", got)
				}
				history, ok := proxy.history(transform.historySessionID, "call-shell")
				if !ok || history.TranslationError != shellTypeScriptDiagnostic || history.ReplayCarrier {
					t.Fatalf("rejection history: %+v", history)
				}
				replay, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"input": []any{carrier, map[string]any{"type": carrierOutputItemType(history.effectiveCarrierKind()), "call_id": "call-shell", "output": shellTypeScriptDiagnostic}}}))
				if err != nil {
					t.Fatal(err)
				}
				if err := proxy.reconcileInputPrefix(&replay, transform.historySessionID); err != nil {
					t.Fatal(err)
				}
				var replayed []map[string]json.RawMessage
				if err := json.Unmarshal(replay.fields["input"], &replayed); err != nil {
					t.Fatal(err)
				}
				if jsonString(replayed[0], "name") != "shell" || jsonString(replayed[0], "input") != input || !strings.Contains(jsonString(replayed[1], "output"), "shell-typescript-misuse") {
					t.Fatal("replay lost original call or diagnostic")
				}
			})
		}
	}
}
