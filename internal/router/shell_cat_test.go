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

func TestShellCatStreamingKeepsOneReplayableCarrier(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
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
	if !ok || history.Script != source || strings.Contains(history.carrierInput(), "apply_patch") {
		t.Fatalf("retained carrier = %#v", history)
	}
	if strings.Count(visible.String(), "event: response.output_item.added") != 1 || strings.Count(visible.String(), "event: response.output_item.done") != 1 {
		t.Fatalf("extra client-side calls: %s", visible.String())
	}
}
