package router

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellBatchExecutionAndReplay(t *testing.T) {
	t.Parallel()
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	directory := t.TempDir()
	transform.directory = directory
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "shell"), []byte("#!/bin/sh\ninterpreter=$1\nshift\nexec \"$interpreter\" -c \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	workerPath := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	source := "#!batch=NEXT\n#!params=" + string(mustMarshalJSON(map[string]any{"workdir": directory})) +
		"\nprintf before; exit 7\nNEXT\n#!python3\nfrom pathlib import Path\nPath('order').write_text('python')\nprint('middle')\nNEXT\n" +
		"#!params=" + string(mustMarshalJSON(map[string]any{"workdir": directory, "yield_time_ms": 1000})) +
		"\ncat order; cat > out <<'EOF'\nliteral\nEOF\n"
	upstream := map[string]json.RawMessage{
		"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON("shell"),
		"call_id": mustMarshalJSON("batch-call"), "input": mustMarshalJSON(source),
		"status": mustMarshalJSON("completed"), "future": mustMarshalJSON("preserved"),
	}
	contribution, _ := proxy.registry.contribution("shell")
	history, err := transform.translateRegisteredTool(contribution, "batch-call", source, upstream)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate = %+v, %v", history, err)
	}
	if strings.Count(history.carrierInput(), "await tools.apply_patch(") != 1 {
		t.Fatalf("lost cat projection: %s", history.carrierInput())
	}
	var result struct {
		Results []struct {
			Output   string `json:"output"`
			ExitCode int    `json:"exit_code"`
		} `json:"results"`
		Retained  bool   `json:"retained"`
		ScriptRef string `json:"script_ref"`
	}
	runShellCatJavaScript(t, proxy.registry.NodeExecutable, directory, history.carrierInput(), &result, "", "PATH="+workerPath)
	if len(result.Results) != 3 || result.Results[0].Output != "before" || result.Results[0].ExitCode != 7 ||
		result.Results[1].Output != "middle\n" || result.Results[1].ExitCode != 0 ||
		result.Results[2].Output != "python" || result.Results[2].ExitCode != 0 {
		t.Fatalf("batch result = %+v", result)
	}
	content, err := os.ReadFile(filepath.Join(directory, "out"))
	if err != nil || string(content) != "literal\n" {
		t.Fatalf("written content = %q, %v", content, err)
	}
	if !result.Retained {
		t.Fatal("batch not retained")
	}
	resolved, err := proxy.resolveShellInput(transform.shellDirectory, "#!script="+result.ScriptRef)
	if err != nil || resolved != source {
		t.Fatalf("retained batch = %q, %v", resolved, err)
	}
	rerun, err := transform.translateRegisteredTool(contribution, "batch-rerun", "#!script="+result.ScriptRef, nil)
	if err != nil || rerun.TranslationError != "" || !strings.Contains(rerun.carrierInput(), "const results = [];") {
		t.Fatalf("batch rerun = %+v, %v", rerun, err)
	}

	if err := proxy.rememberBatch(transform.historySessionID, transform.local); err != nil {
		t.Fatal(err)
	}
	carrier := map[string]json.RawMessage{
		"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON(history.CarrierName),
		"call_id": mustMarshalJSON("batch-call"), "input": mustMarshalJSON(history.carrierInput()),
	}
	output := map[string]json.RawMessage{"type": mustMarshalJSON("custom_tool_call_output"), "call_id": mustMarshalJSON("batch-call"), "output": mustMarshalJSON("batch results")}
	request, err := parseResponsesRequest(mustMarshalJSON(map[string]any{"input": []any{carrier, output}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.reconcileInputPrefix(&request, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	var replay []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &replay); err != nil {
		t.Fatal(err)
	}
	if string(mustMarshalJSON(replay[0])) != string(mustMarshalJSON(upstream)) || jsonString(replay[1], "output") != "batch results" {
		t.Fatalf("provider replay changed: %s", request.fields["input"])
	}
}

func TestShellBatchStopPolicyExecutionAndRetention(t *testing.T) {
	t.Parallel()
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	directory := t.TempDir()
	transform.directory = directory
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "shell"), []byte("#!/bin/sh\ninterpreter=$1\nshift\nexec \"$interpreter\" -c \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	workerPath := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	source := "#!batch-stop=NEXT\n#!params=" + string(mustMarshalJSON(map[string]any{"workdir": directory})) +
		"\nprintf failed; exit 7\nNEXT\ntouch should-not-run"
	contribution, _ := proxy.registry.contribution("shell")
	history, err := transform.translateRegisteredTool(contribution, "batch-stop", source, nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate: %+v, %v", history, err)
	}
	var result struct {
		Results   []map[string]any `json:"results"`
		ScriptRef string           `json:"script_ref"`
		Batch     struct {
			Policy     string `json:"on_nonzero_exit"`
			Total      int    `json:"program_count"`
			Started    int    `json:"started_programs"`
			NotStarted int    `json:"not_started_programs"`
			Reason     string `json:"stopped_reason"`
		} `json:"batch"`
	}
	runShellCatJavaScript(t, proxy.registry.NodeExecutable, directory, history.carrierInput(), &result, "", "PATH="+workerPath)
	if len(result.Results) != 1 || result.Results[0]["exit_code"] != float64(7) ||
		result.Batch.Policy != "stop" || result.Batch.Total != 2 || result.Batch.Started != 1 ||
		result.Batch.NotStarted != 1 || result.Batch.Reason != "nonzero_exit" {
		t.Fatalf("stop result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(directory, "should-not-run")); !os.IsNotExist(err) {
		t.Fatalf("later program executed: %v", err)
	}
	resolved, err := proxy.resolveShellInput(transform.shellDirectory, "#!script="+result.ScriptRef)
	if err != nil || resolved != source {
		t.Fatalf("retained policy lost: %q, %v", resolved, err)
	}
	rerun, err := transform.translateRegisteredTool(contribution, "batch-stop-rerun", "#!script="+result.ScriptRef, nil)
	if err != nil || rerun.TranslationError != "" || !strings.Contains(rerun.carrierInput(), "break batch") {
		t.Fatalf("rerun lost stop policy: %+v, %v", rerun, err)
	}
}

func TestShellBatchStopWaitsForTerminalExit(t *testing.T) {
	t.Parallel()
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	contribution, _ := proxy.registry.contribution("shell")
	history, err := transform.translateRegisteredTool(contribution, "batch-stop-wait",
		"#!batch-stop=NEXT\nsleep 100\nNEXT\necho later", nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate: %+v, %v", history, err)
	}
	overrides := `
let executions = 0, waits = 0;
tools.exec_command = async () => {
  if (++executions !== 1) throw new Error('later program ran');
  return {output:'start',session_id:42};
};
tools.write_stdin = async args => {
  if (++waits !== 1 || args.session_id !== 42) throw new Error('wrong continuation');
  return {output:' end',exit_code:9};
};`
	var result struct {
		Results []map[string]any `json:"results"`
		Batch   map[string]any   `json:"batch"`
	}
	runShellCatJavaScript(t, proxy.registry.NodeExecutable, t.TempDir(), history.carrierInput(), &result, overrides)
	if len(result.Results) != 1 || result.Results[0]["output"] != "start end" ||
		result.Results[0]["exit_code"] != float64(9) || result.Batch["not_started_programs"] != float64(1) {
		t.Fatalf("terminal stop: %+v", result)
	}
}

func TestShellBatchParamsAndContinuation(t *testing.T) {
	t.Parallel()
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	contribution, _ := proxy.registry.contribution("shell")
	source := "#!batch=NEXT\n#!params={\"workdir\":\"/tmp\",\"yield_time_ms\":1000,\"max_output_tokens\":123}\necho one\nNEXT\n" +
		"#!python3\nprint(2)\nNEXT\n#!params={\"yield_time_ms\":2000}\necho three"
	history, err := transform.translateRegisteredTool(contribution, "batch-params", source, nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate = %+v, %v", history, err)
	}
	overrides := `
let calls = 0, continuations = 0, running = false;
tools.exec_command = async args => {
  if (running) throw new Error('overlapping programs');
  calls++;
  if (args.login !== false) throw new Error('login default lost');
  if (calls < 3) {
    if (args.workdir !== '/tmp' || args.yield_time_ms !== 1000 || args.max_output_tokens !== 123)
      throw new Error('inherited params lost');
  } else if (calls === 3) {
    if ('workdir' in args || 'max_output_tokens' in args || args.yield_time_ms !== 2000)
      throw new Error('new params did not replace previous object');
  } else throw new Error('unexpected execution');
  if (calls === 1) {
    running = true;
    return {session_id: 42, output: 'start'};
  }
  return {output: String(calls), exit_code: 0, future: calls};
};
tools.write_stdin = async args => {
  if (!running || args.session_id !== 42 || args.chars !== '' || args.max_output_tokens !== 123)
    throw new Error('wrong continuation');
  continuations++;
  if (continuations === 1) return {session_id: 42, output: ' progress'};
  running = false;
  return {output: ' done', exit_code: 9, future: 'terminal'};
};
`
	var result struct {
		Results []map[string]any `json:"results"`
	}
	runShellCatJavaScript(t, proxy.registry.NodeExecutable, transform.directory, history.carrierInput(), &result, overrides)
	if len(result.Results) != 3 || result.Results[0]["output"] != "start progress done" ||
		result.Results[0]["exit_code"] != float64(9) || result.Results[0]["future"] != "terminal" ||
		result.Results[1]["future"] != float64(2) || result.Results[2]["future"] != float64(3) {
		t.Fatalf("results = %+v", result.Results)
	}
	if _, yielded := result.Results[0]["session_id"]; yielded {
		t.Fatal("terminal result retained a stale session handle")
	}
}

func TestShellBatchRejectsBeforeExecution(t *testing.T) {
	t.Parallel()
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	contribution, _ := proxy.registry.contribution("shell")
	for _, test := range []struct {
		name, source, diagnostic string
		native                   bool
	}{
		{"missing separator", "#!batch=NEXT\necho first", "at least two programs", false},
		{"empty separator", "#!batch=\necho first", "separator must be nonempty", false},
		{"trailing separator", "#!batch=NEXT\necho first\nNEXT\n", "shell program 2: line 1: batch programs must have a body", false},
		{"bad later directive", "#!batch=NEXT\necho first\nNEXT\n#!python3\n#!cmd missing\nprint(1)", "shell program 2: line 2:", false},
		{"bad later JSON", "#!batch=NEXT\necho first\nNEXT\n#!params={bad}\necho second", "JSON object", false},
		{"unsafe later params", "#!batch=NEXT\necho first\nNEXT\n#!params={\"login\":true}\necho second", "shell program 2: line 1: #!params login", false},
		{"cmd param", "#!batch=NEXT\necho first\nNEXT\n#!params={\"cmd\":\"override\"}\necho second", "shell program 2: line 1: #!params must not contain cmd", false},
		{"misplaced Code Mode", "#!batch=NEXT\necho first\nNEXT\n#!bash\nconst r = await tools.exec_command({cmd: 'echo second'}); text(r);", "shell-typescript-misuse", false},
		{"empty later program", "#!batch=NEXT\necho first\nNEXT\n#!python3\n", "must have a body", false},
		{"native batch", "#!batch=NEXT\necho first\nNEXT\n#!python3\nprint(2)", "require Code Mode", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
			transform.nativeTools = test.native
			if test.native {
				transform.codeModeToolName = "exec_command"
				transform.carriers = codeModeCarrierCatalog{"exec_command": codeModeCarrierFunction}
			}
			history, err := transform.translateRegisteredTool(contribution, "reject-"+strings.ReplaceAll(test.name, " ", "-"), test.source, nil)
			if err != nil || !strings.Contains(history.TranslationError, test.diagnostic) {
				t.Fatalf("rejection = %+v, %v", history, err)
			}
			if strings.Contains(history.carrierInput(), "await tools.exec_command(") ||
				strings.Contains(history.carrierInput(), "echo first") {
				t.Fatalf("rejection contains executable prefix: %s", history.carrierInput())
			}
		})
	}
}
func TestShellBatchPreservesPartialResultsOnHostFailure(t *testing.T) {
	t.Parallel()
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	contribution, _ := proxy.registry.contribution("shell")
	history, err := transform.translateRegisteredTool(contribution, "host-failure", "#!batch=NEXT\necho one\nNEXT\n#!params={}\necho two\nNEXT\n#!params={}\necho three", nil)
	if err != nil {
		t.Fatal(err)
	}
	program := `
const emissions = [];
const text = value => emissions.push(JSON.parse(value));
let calls = 0;
const tools = {
  exec_command: async () => {
    calls++;
    if (calls === 1) return {output: 'one', exit_code: 0};
    if (calls === 2) return {output: 'partial', session_id: 42};
    throw new Error('suffix ran');
  },
  write_stdin: async () => { throw new Error('host refused'); }
};
(async () => {
` + history.carrierInput() + `
})().then(() => { throw new Error('failure swallowed'); }).catch(error => {
  process.stdout.write(JSON.stringify({emissions, calls, error: error.message}));
});
`
	command := exec.CommandContext(t.Context(), proxy.registry.NodeExecutable, "-e", program)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, output)
	}
	var result struct {
		Emissions []struct {
			Batch    map[string]any   `json:"batch"`
			Results  []map[string]any `json:"results"`
			Retained bool             `json:"retained"`
		} `json:"emissions"`
		Calls int    `json:"calls"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Calls != 2 || result.Error != "host refused" || len(result.Emissions) != 1 ||
		!result.Emissions[0].Retained || len(result.Emissions[0].Results) != 2 ||
		result.Emissions[0].Results[0]["output"] != "one" ||
		result.Emissions[0].Results[1]["output"] != "partial" ||
		result.Emissions[0].Results[1]["session_id"] != float64(42) {
		t.Fatalf("host failure = %+v", result)
	}
	summary := result.Emissions[0].Batch
	if summary["on_nonzero_exit"] != "continue" || summary["stopped_reason"] != "host_error" ||
		summary["started_programs"] != float64(2) || summary["not_started_programs"] != float64(1) {
		t.Fatalf("host failure summary = %+v", summary)
	}
}

func TestShellBatchJSONAndStreamingKeepOneCarrier(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	source := "#!batch=NEXT\n#!params={\"yield_time_ms\":1000}\necho one\nNEXT\n#!python3\nprint(2)"
	item := map[string]any{"id": "batch-item", "call_id": "batch-stream", "type": "custom_tool_call", "name": "shell", "input": source, "status": "completed"}
	for _, streaming := range []bool{false, true} {
		transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
		if !streaming {
			visible, err := transform.TransformJSON(mustMarshalJSON(map[string]any{"status": "completed", "output": []any{item}}))
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Output []map[string]json.RawMessage `json:"output"`
			}
			if err := json.Unmarshal(visible, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Output) != 1 || jsonString(response.Output[0], "call_id") != "batch-stream" ||
				jsonString(response.Output[0], "name") != transform.codeModeToolName ||
				!strings.Contains(jsonString(response.Output[0], "input"), "const results = [];") {
				t.Fatalf("JSON carrier = %s", visible)
			}
			continue
		}
		added := map[string]any{"id": "batch-item", "call_id": "batch-stream", "type": "custom_tool_call", "name": "shell", "input": "", "status": "in_progress"}
		events := []map[string]any{
			{"type": "response.output_item.added", "item": added},
			{"type": "response.custom_tool_call_input.delta", "item_id": "batch-item", "delta": source[:17]},
			{"type": "response.custom_tool_call_input.delta", "item_id": "batch-item", "delta": source[17:]},
			{"type": "response.custom_tool_call_input.done", "item_id": "batch-item", "input": source},
			{"type": "response.output_item.done", "item": item},
			{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{item}}},
		}
		var body strings.Builder
		for _, event := range events {
			body.WriteString("event: " + event["type"].(string) + "\ndata: " + string(mustMarshalJSON(event)) + "\n\n")
		}
		var visible bytes.Buffer
		terminal, err := copySSETransformed(&visible, strings.NewReader(body.String()), transform, nil)
		if err != nil || terminal != responseTerminalCompleted {
			t.Fatalf("stream = %v, %v: %s", terminal, err, visible.String())
		}
		history, ok := proxy.history(transform.historySessionID, "batch-stream")
		if !ok || history.Script != source || !strings.Contains(history.carrierInput(), "const results = [];") {
			t.Fatalf("history = %+v", history)
		}
		if strings.Count(visible.String(), "event: response.output_item.added") != 1 ||
			strings.Count(visible.String(), "event: response.output_item.done") != 1 {
			t.Fatalf("extra client-side calls: %s", visible.String())
		}
	}
}

func TestShellNativeSourceMarkersExecuteUnchanged(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	directory := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "shell"), []byte("#!/bin/sh\ninterpreter=$1\nshift\nexec \"$interpreter\" -c \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	contribution, _ := proxy.registry.contribution("shell")
	const markers = "#!python3\n#!params={bad}\n#!script=@shell/example\n#!batch=NEXT\nNEXT\n"
	python := "#!python3\nprint('''" + markers + "''', end='')"
	bash := "cat <<'SOURCE'\n" + markers + "SOURCE\n"
	for _, test := range []struct {
		name, input string
		batch       bool
	}{
		{"python", python, false},
		{"bash heredoc", bash, false},
		{"explicit batch", "#!batch=--program--\n" + python + "\n--program--\n" + bash, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
			transform.directory = directory
			history, err := transform.translateRegisteredTool(contribution, "native-source-"+strings.ReplaceAll(test.name, " ", "-"), test.input, nil)
			if err != nil || history.TranslationError != "" {
				t.Fatalf("translation = %+v, %v", history, err)
			}
			var result struct {
				Output  string `json:"output"`
				Results []struct {
					Output string `json:"output"`
				} `json:"results"`
			}
			runShellCatJavaScript(t, proxy.registry.NodeExecutable, directory, history.carrierInput(), &result, "")
			if !test.batch {
				if result.Output != markers || len(result.Results) != 0 {
					t.Fatalf("single source changed or split: %+v", result)
				}
			} else if len(result.Results) != 2 || result.Results[0].Output != markers || result.Results[1].Output != markers {
				t.Fatalf("batch source changed: %+v", result)
			}
		})
	}
}
