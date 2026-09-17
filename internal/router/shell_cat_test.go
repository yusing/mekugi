package router

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

func TestSplitShellCatWrites(t *testing.T) {
	for _, test := range []struct {
		name, source, content string
		steps                 int
	}{
		{"single", "cat > out <<'EOF'\nhello\nEOF\n", "hello\n", 1},
		{"sequence", "foo; cat << 'EOF' > out\nhello; world\nEOF\nbar;", "hello; world\n", 3},
		{"quoted", "cat > 'two words' <<\"EOF\"\n$HOME `whoami` \\n\nEOF\n", "$HOME `whoami` \\n\n", 1},
		{"tabs", "cat 1>out 0<<-'EOF'\n\tfoo\n\t\tbar\n\tEOF\n", "foo\nbar\n", 1},
		{"empty", "cat > out <<'EOF'\nEOF\n", "", 1},
		{"blank lines", "cat > out <<'EOF'\n\nhello\n\nEOF\n", "\nhello\n\n", 1},
		{"single quoted metacharacters", "cat > 'notes[1]~*?' <<'EOF'\nhello\nEOF\n", "hello\n", 1},
		{"double quoted metacharacters", "cat > \"notes[1]~*?\" <<'EOF'\nhello\nEOF\n", "hello\n", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, variant := range []syntax.LangVariant{syntax.LangBash, syntax.LangPOSIX} {
				steps, ok := splitShellCatWrites(test.source, "/workspace", variant)
				if !ok || len(steps) != test.steps {
					t.Fatalf("steps = %#v, converted = %v", steps, ok)
				}
				found := false
				for _, step := range steps {
					if step.patch == "" {
						continue
					}
					found = true
					var content strings.Builder
					for line := range strings.SplitSeq(step.patch, "\n") {
						if strings.HasPrefix(line, "+") {
							content.WriteString(line[1:] + "\n")
						}
					}
					if content.String() != test.content {
						t.Fatalf("content = %q, want %q", content.String(), test.content)
					}
				}
				if !found {
					t.Fatal("no patch step")
				}
			}
		})
	}
}

func TestSplitShellCatWritesLeavesUnsupportedShellUnchanged(t *testing.T) {
	write := "cat > out <<'EOF'\nhello\nEOF\n"
	for _, source := range []string{
		"foo && " + write, "foo || " + write, "foo & " + write,
		"foo | " + write, "! " + write, "(" + write + ")",
		"if true; then " + write + "fi", "for i in 1; do " + write + "done",
		"cd elsewhere; " + write, "export X=1; " + write, "X=1; " + write,
		"set -e; " + write, "f() { true; }; " + write,
		"printf '%s' \"$?\"; " + write, "echo $(date); " + write,
		"cat >>out <<'EOF'\nhello\nEOF\n", "cat source >out",
		"cat >out <<EOF\n$HOME\nEOF\n", "cat >out <<EOF\nhello\nEOF\n",
		"cat >\"$OUT\" <<'EOF'\nhello\nEOF\n", "cat >*.txt <<'EOF'\nhello\nEOF\n",
		"cat >out <<'EOF'\r\nhello\r\nEOF\r\n", "cat <<'EOF'\nhello\nEOF\n",
		"cat >existing/. <<'EOF'\nhello\nEOF\n", "cat >missing/. <<'EOF'\nhello\nEOF\n",
		"cat >. <<'EOF'\nhello\nEOF\n", "cat >link/../out <<'EOF'\nhello\nEOF\n",
	} {
		if _, ok := splitShellCatWrites(source, "/workspace", syntax.LangBash); ok {
			t.Errorf("split unsupported script %q", source)
		}
	}
	if _, ok := splitShellCatWrites(write, "", syntax.LangBash); ok {
		t.Fatal("used an unknown working directory")
	}
}

func TestShellCatCarrierExecutionAndReplay(t *testing.T) {
	t.Parallel()
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	session := transform.historySessionID
	directory := t.TempDir()
	transform.directory = directory
	// The host fixture executes only the simple shell statements accepted here.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "shell"), []byte("#!/bin/sh\ninterpreter=$1\nshift\nexec \"$interpreter\" -c \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	workerPath := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	source := "printf before; mkdir nested; cat > nested/out <<'EOF'\n$HOME; literal\nEOF\nprintf after; cat > nested/out <<'EOF'\nreplacement\nEOF\n"
	upstream := map[string]json.RawMessage{
		"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON("shell"),
		"call_id": mustMarshalJSON("cat-write"), "input": mustMarshalJSON(source),
		"status": mustMarshalJSON("completed"), "future": mustMarshalJSON("preserved"),
	}
	contribution, _ := proxy.registry.contribution("shell")
	history, err := transform.translateRegisteredTool(contribution, "cat-write", source, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(history.carrierInput(), "await tools.apply_patch(") != 2 || strings.Contains(history.carrierInput(), "Warning:") {
		t.Fatalf("missing native patch calls: %s", history.carrierInput())
	}
	var result struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
		Retained bool   `json:"retained"`
	}
	runShellCatJavaScript(t, proxy.registry.NodeExecutable, directory, history.carrierInput(), &result, "", "PATH="+workerPath)
	if result.Output != "beforeafter" || result.ExitCode != 0 {
		t.Fatalf("shell result = %+v", result)
	}
	content, err := os.ReadFile(filepath.Join(directory, "nested", "out"))
	if err != nil || string(content) != "replacement\n" {
		t.Fatalf("written content = %q, error %v", content, err)
	}
	if err := proxy.rememberBatch(session, transform.local); err != nil {
		t.Fatal(err)
	}
	carrier := map[string]json.RawMessage{
		"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON(history.CarrierName),
		"call_id": mustMarshalJSON("cat-write"), "input": mustMarshalJSON(history.carrierInput()),
	}
	output := map[string]json.RawMessage{"type": mustMarshalJSON("custom_tool_call_output"), "call_id": mustMarshalJSON("cat-write"), "output": mustMarshalJSON("original result")}
	request, err := parseResponsesRequest(mustMarshalJSON(map[string]any{"input": []any{carrier, output}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.reconcileInputPrefix(&request, session); err != nil {
		t.Fatal(err)
	}
	var replay []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &replay); err != nil {
		t.Fatal(err)
	}
	if string(mustMarshalJSON(replay[0])) != string(mustMarshalJSON(upstream)) || jsonString(replay[1], "output") != "original result" {
		t.Fatalf("provider replay changed: %s", request.fields["input"])
	}
}

func runShellCatJavaScript(t *testing.T, node, directory, carrier string, result any, overrides string, environment ...string) {
	t.Helper()
	program := `const {spawnSync} = require('node:child_process');
const fs = require('node:fs');
const tools = {
  exec_command: async args => {
    // Carrier arguments use Bash ANSI-C quoting, including escaped heredoc newlines.
    const child = spawnSync('bash', ['-c', args.cmd], {cwd: args.workdir || process.cwd(), encoding: 'utf8'});
    if (child.error) throw child.error;
    return {output: child.stdout + child.stderr, exit_code: child.status};
  },
  apply_patch: async patch => {
    const lines = patch.split('\n');
    const path = lines[1].slice('*** Add File: '.length);
    let content = '';
    for (const line of lines.slice(2, -2)) {
      if (!line.startsWith('+')) throw new Error('invalid patch row');
      content += line.slice(1) + '\n';
    }
    fs.writeFileSync(path, content);
    return {};
  }
};
const text = value => process.stdout.write(value);
const fixtureExec = tools.exec_command;
` + overrides + `
const scenarioExec = tools.exec_command;
const scenarioWrite = tools.write_stdin;
const controlSessions = new Map();
let nextControlSession = 900000;
tools.exec_command = async args => {
  if (args.cmd !== 'shell') return scenarioExec(args);
  if (!args.tty || args.login !== false) throw new Error('invalid control carrier');
  const {spawn} = require('node:child_process');
  const child = spawn('bash', ['-c', args.cmd], {cwd: args.workdir || process.cwd()});
  const session_id = nextControlSession++;
  let output = '';
  let exitCode;
  let wake;
  child.stdout.on('data', chunk => { output += chunk; wake?.(); });
  child.stderr.on('data', chunk => { output += chunk; wake?.(); });
  child.on('error', error => { output += String(error); exitCode = 1; wake?.(); });
  child.on('close', code => { exitCode = code; wake?.(); });
  async function read() {
    while (exitCode === undefined && !output.endsWith('\n')) {
      await new Promise(resolve => { wake = resolve; });
    }
    const result = {output};
    output = '';
    if (exitCode === undefined) result.session_id = session_id;
    else { result.exit_code = exitCode; controlSessions.delete(session_id); }
    return result;
  }
  controlSessions.set(session_id, {child, read});
  return read();
};
tools.write_stdin = async args => {
  const receiver = controlSessions.get(args.session_id);
  if (!receiver && args.chars === '{"operation":"close"}\n') return {exit_code: 0, output: ''};
  if (!receiver) return scenarioWrite(args);
  if (args.chars) receiver.child.stdin.write(args.chars);
  return receiver.read();
};
` + "\n(async () => {\n" + carrier + "\n})().catch(error => { console.error(error); process.exitCode = 1; });"
	carrierPath := filepath.Join(t.TempDir(), "carrier.cjs")
	if err := os.WriteFile(carrierPath, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), node, carrierPath)
	command.Dir = directory
	if len(environment) != 0 {
		command.Env = append(os.Environ(), environment...)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute carrier: %v\n%s", err, output)
	}
	if err := json.Unmarshal(output, result); err != nil {
		t.Fatalf("decode carrier result: %v\n%s", err, output)
	}
}

func TestShellCatCarrierWaitsBeforeApplying(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	transform.directory = t.TempDir()
	contribution, _ := proxy.registry.contribution("shell")
	carrier, ok := transform.shellCatCarrier(contribution, codeModeCarrierCustom, []string{"bash", "foo; cat >out <<'EOF'\nhello\nEOF\nbar"}, "", nil)
	if !ok {
		t.Fatal("no split")
	}
	overrides := `let pending = false;
let calls = 0;
tools.exec_command = async () => {
  if (pending) throw new Error('advanced past a live command');
  calls++;
  if (calls === 1) { pending = true; return {session_id: 42, output: 'start'}; }
  return {exit_code: 0, output: calls === 2 ? '' : 'end'};
};
tools.write_stdin = async args => {
  if (!pending || args.session_id !== 42 || args.yield_time_ms !== 300000) throw new Error('bad continuation');
  pending = false;
  return {exit_code: 7, output: 'finished'};
};
tools.apply_patch = async () => {
  if (pending || calls !== 2) throw new Error('patch order changed');
  return {};
};`
	var result map[string]any
	runShellCatJavaScript(t, proxy.registry.NodeExecutable, transform.directory, carrier, &result, overrides)
	if result["output"] != "startfinishedend" || result["exit_code"] != float64(0) {
		t.Fatalf("result = %#v", result)
	}
}

func TestShellCatCarrierRuntimeFallbacks(t *testing.T) {
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
	if err := os.Symlink("target", filepath.Join(directory, "link")); err != nil {
		t.Fatal(err)
	}
	contribution, _ := proxy.registry.contribution("shell")
	for _, path := range []string{"link", "missing/child"} {
		t.Run(path, func(t *testing.T) {
			source := "cat > " + path + " <<'EOF'\nliteral\nEOF\n"
			carrier, ok := transform.shellCatCarrier(contribution, codeModeCarrierCustom, []string{"bash", source}, "", nil)
			if !ok {
				t.Fatal("missing carrier")
			}
			var result struct {
				ExitCode int `json:"exit_code"`
			}
			runShellCatJavaScript(t, proxy.registry.NodeExecutable, directory, carrier, &result,
				"tools.apply_patch = async () => { throw new Error('unsafe target reached patch tool'); };",
				"PATH="+workerPath)
			if path == "link" {
				content, err := os.ReadFile(filepath.Join(directory, "target"))
				if err != nil || string(content) != "literal\n" || result.ExitCode != 0 {
					t.Fatalf("symlink write = %q, status %d, error %v", content, result.ExitCode, err)
				}
			} else if result.ExitCode == 0 {
				t.Fatal("missing parent unexpectedly succeeded")
			}
		})
	}
}

func TestShellCatNativeCarrierWithHostApplyPatch(t *testing.T) {
	applyPatch, err := exec.LookPath("apply_patch")
	if err != nil {
		t.Skip("host apply_patch executable is not installed")
	}
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _ := newNativeMekugiTestTransformWithProxy(t, proxy)
	transform.directory = t.TempDir()
	contribution, _ := proxy.registry.contribution("shell")
	// Use the real host parser/application, including an overwrite and an empty file.
	for _, content := range []string{"before\n", "after\n\n", ""} {
		source := "cat > out <<'EOF'\n" + content + "EOF\n"
		carrier, ok := transform.shellCatCarrier(contribution, codeModeCarrierFunction, []string{"bash", source}, "", nil)
		if !ok {
			t.Fatal("missing native carrier")
		}
		var arguments map[string]json.RawMessage
		if err := json.Unmarshal([]byte(carrier), &arguments); err != nil {
			t.Fatal(err)
		}
		// Execute the outer carrier with Bash, matching its canonical argument quoting.
		command := exec.CommandContext(t.Context(), "bash", "-c", jsonString(arguments, "cmd"))
		command.Dir = transform.directory
		command.Env = append(os.Environ(), "PATH="+filepath.Dir(applyPatch)+string(os.PathListSeparator)+os.Getenv("PATH"))
		output, err := command.CombinedOutput()
		if err != nil || len(output) != 0 {
			t.Fatalf("native patch: %v, output %q", err, output)
		}
		written, err := os.ReadFile(filepath.Join(transform.directory, "out"))
		if err != nil || string(written) != content {
			t.Fatalf("native content = %q, want %q; error %v", written, content, err)
		}
	}
}

func TestShellCatStreamingKeepsOneReplayableCarrier(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	source := "foo; cat > out <<'EOF'\nliteral\nEOF\nbar"
	item := map[string]any{"id": "cat-item", "call_id": "cat-call", "type": "custom_tool_call", "name": "shell", "input": source, "status": "completed"}
	added := map[string]any{"id": "cat-item", "call_id": "cat-call", "type": "custom_tool_call", "name": "shell", "input": "", "status": "in_progress"}
	events := []map[string]any{
		{"type": "response.output_item.added", "item": added},
		{"type": "response.custom_tool_call_input.delta", "item_id": "cat-item", "delta": source},
		{"type": "response.custom_tool_call_input.done", "item_id": "cat-item", "input": source},
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
		t.Fatalf("stream = %v, error %v: %s", terminal, err, visible.String())
	}
	history, ok := proxy.history(transform.historySessionID, "cat-call")
	if !ok || history.Script != source || strings.Count(history.carrierInput(), "await tools.apply_patch(") != 1 {
		t.Fatalf("retained carrier = %#v", history)
	}
	if strings.Count(visible.String(), "event: response.output_item.added") != 1 || strings.Count(visible.String(), "event: response.output_item.done") != 1 {
		t.Fatalf("extra client-side calls: %s", visible.String())
	}
}

func TestShellCatCarrierPreservesOutputOnHostFailure(t *testing.T) {
	t.Parallel()
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	contribution, _ := proxy.registry.contribution("shell")
	carrier, ok := transform.shellCatCarrier(contribution, codeModeCarrierCustom,
		[]string{"bash", "foo; cat > out <<'EOF'\nliteral\nEOF\nbar"}, "", nil)
	if !ok {
		t.Fatal("no split")
	}
	for _, failure := range []string{"patch", "continuation"} {
		t.Run(failure, func(t *testing.T) {
			program := "const failure = " + string(mustMarshalJSON(failure)) + `;
const emissions = [];
const text = value => emissions.push(JSON.parse(value));
let calls = 0;
const tools = {
  exec_command: async () => {
    calls++;
    if (calls === 1) return failure === 'continuation'
      ? {session_id: 42, output: 'prefix diagnostics'}
      : {exit_code: 0, output: 'prefix diagnostics'};
    if (calls !== 2) throw new Error('suffix executed after failure');
    return {exit_code: 0, output: ''};
  },
  write_stdin: async () => { throw new Error('continuation failed'); },
  apply_patch: async () => { throw new Error('patch refused'); }
};
(async () => {
` + carrier + `
})().then(() => { throw new Error('failure was swallowed'); }).catch(error => {
  process.stdout.write(JSON.stringify({emissions, error: error.message, calls}));
});`
			command := exec.CommandContext(t.Context(), proxy.registry.NodeExecutable, "-e", program)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("execute failure carrier: %v\n%s", err, output)
			}
			var result struct {
				Emissions []struct {
					Output    string `json:"output"`
					SessionID int    `json:"session_id"`
				} `json:"emissions"`
				Error string `json:"error"`
				Calls int    `json:"calls"`
			}
			if err := json.Unmarshal(output, &result); err != nil {
				t.Fatal(err)
			}
			wantCalls, wantError := 2, "patch refused"
			if failure == "continuation" {
				wantCalls, wantError = 1, "continuation failed"
			}
			if result.Calls != wantCalls || result.Error != wantError || len(result.Emissions) != 1 {
				t.Fatalf("failure result = %+v", result)
			}
			if result.Emissions[0].Output != "prefix diagnostics" {
				t.Fatalf("lost completed output: %+v", result)
			}
			if failure == "continuation" && result.Emissions[0].SessionID != 42 {
				t.Fatalf("lost native continuation handle: %+v", result)
			}
		})
	}
}
