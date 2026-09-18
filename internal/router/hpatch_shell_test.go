package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/patchtest"
)

// This private worker only translates. Codex still authorizes and applies the
// returned patch, after every preceding host execution has finished.
func runHpatchTranslation(ctx context.Context, directory, source string, stdout io.Writer) error {
	result, _, err := translateHpatchSegment(ctx, directory, source)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}

type mixedScriptResult struct {
	ChangeID     string `json:"change_id"`
	ResumeHandle string `json:"resume_handle"`
	Results      []struct {
		Repair     bool   `json:"repair"`
		Segment    int    `json:"segment"`
		Line       int    `json:"line"`
		Kind       string `json:"kind"`
		Status     string `json:"status"`
		Output     string `json:"output"`
		Report     string `json:"report"`
		Diagnostic string `json:"diagnostic"`
		ExitCode   int    `json:"exit_code"`
		SessionID  int    `json:"session_id"`
	} `json:"results"`
	Sequence struct {
		Count      int    `json:"segment_count"`
		Started    int    `json:"started_segments"`
		NotStarted int    `json:"not_started_segments"`
		Stopped    string `json:"stopped_reason"`
	} `json:"sequence"`
}

// A subprocess exercises runtime translation against the files left by earlier
// shell programs. Patch application uses the repository's existing host harness.
func TestHpatchMixedProcess(t *testing.T) {
	if os.Getenv("MEKUGI_HPATCH_WORKER_TEST") != "1" || slices.Index(os.Args, "--") < 0 {
		return
	}
	ctx := t.Context()
	if os.Getenv("MEKUGI_HPATCH_CANCEL_TRANSFER") == "1" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
	}
	if err := runHpatchControl(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// Keep mock host patch application in the owning test process. Translation still
// uses the real subprocess/control protocol; each patch uses the same host harness.
func applyMixedTestPatch(directory, patch string) error {
	initial := map[string]string{}
	if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		content, err := os.ReadFile(path)
		initial[path] = string(content)
		return err
	}); err != nil {
		return err
	}
	lines := strings.Split(patch, "\n")
	for index, line := range lines {
		for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
			if path, ok := strings.CutPrefix(line, prefix); ok && !filepath.IsAbs(path) {
				lines[index] = prefix + filepath.Join(directory, path)
			}
		}
	}
	tree, err := patchtest.Apply(initial, strings.Join(lines, "\n"))
	if err != nil {
		return err
	}
	for path, content := range tree {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return err
		}
	}
	for path := range initial {
		if _, exists := tree[path]; !exists {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

func mixedTestTransform(t *testing.T) (*mekugiResponseTransform, string) {
	t.Helper()
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	transform.directory = t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	runtimeDirectory := filepath.Dir(transform.shellDirectory)
	threadID := strings.TrimPrefix(filepath.Base(transform.shellDirectory), "mekugi-scripts-")
	wrapper := "#!/bin/sh\nexport MEKUGI_RUNTIME_DIR=" + shellQuoteArgument(runtimeDirectory) +
		"\nexport CODEX_THREAD_ID=" + shellQuoteArgument(threadID) +
		"\nif [ \"$#\" = 0 ]; then\nexec " +
		shellQuoteArgument(executable) + " -test.run='^TestHpatchMixedProcess$' --\nfi\ninterpreter=$1\nshift\nexec \"$interpreter\" -c \"$1\"\n"
	if err := os.WriteFile(filepath.Join(bin, "shell"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Directory, Patch string }
		err := json.NewDecoder(r.Body).Decode(&request)
		if err == nil {
			err = applyMixedTestPatch(request.Directory, request.Patch)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	t.Cleanup(host.Close)
	workerPath := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	overrides := `process.env.PATH = ` + string(mustMarshalJSON(workerPath)) + `;
process.env.MEKUGI_HPATCH_WORKER_TEST = '1';
process.env.MEKUGI_RUNTIME_DIR = ` + string(mustMarshalJSON(runtimeDirectory)) + `;
process.env.CODEX_THREAD_ID = ` + string(mustMarshalJSON(threadID)) + `;
tools.apply_patch = patch => new Promise((resolve, reject) => {
  const request = require('node:http').request(` + string(mustMarshalJSON(host.URL)) + `, {method: 'POST'}, response => {
    let diagnostic = '';
    response.setEncoding('utf8');
    response.on('data', chunk => { diagnostic += chunk; });
    response.on('end', () => response.statusCode === 200 ? resolve({}) : reject(new Error(diagnostic)));
  });
  request.on('error', reject);
  request.end(JSON.stringify({Directory: process.cwd(), Patch: patch}));
});`
	return transform, overrides
}

func TestHpatchMixedExecutionAndReplay(t *testing.T) {
	t.Parallel()
	transform, overrides := mixedTestTransform(t)
	source := "shell <<SHELL\nprintf 'draft\\n' > notes.txt\nSHELL\n" +
		"in notes.txt\ntype \"draft\" \"ready\"\n" +
		"shell <<SHELL\ncat notes.txt\nSHELL\n" +
		"in notes.txt\nadd EOF \"checked\\n\"\n" +
		"shell <<SHELL\n#!python3\nprint(open('notes.txt').read(), end='')\nSHELL"
	upstream := map[string]json.RawMessage{
		"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON(mekugiToolName),
		"call_id": mustMarshalJSON("mixed"), "input": mustMarshalJSON(source),
	}
	history, err := transform.translate("mixed", source, upstream)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate = %v, %s", err, history.TranslationError)
	}
	if _, err := os.Stat(filepath.Join(transform.directory, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("preflight performed an execution effect")
	}
	var result mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, history.carrierInput(), &result, overrides)
	if result.Sequence.Count != 5 || result.Sequence.Started != 5 || result.Sequence.NotStarted != 0 || result.Sequence.Stopped != "" ||
		len(result.Results) != 5 || result.Results[2].Output != "ready\n" || result.Results[4].Output != "ready\nchecked\n" ||
		!strings.Contains(result.Results[1].Report, "files add=0 update=1") {
		t.Fatalf("result = %+v", result)
	}
	for index, part := range result.Results {
		if part.Segment != index+1 || part.Status != "completed" {
			t.Fatalf("result = %+v", result)
		}
	}
	if _, err := recoveryHistoryOf(slices.Values([]mekugiHistory{history})); err == nil {
		t.Fatal("mixed execution entered ordinary recovery")
	}
	if err := transform.proxy.rememberBatch(transform.historySessionID, transform.local); err != nil {
		t.Fatal(err)
	}
	request, err := parseResponsesRequest(mustMarshalJSON(map[string]any{"input": []any{
		map[string]any{"type": "custom_tool_call", "name": history.CarrierName, "call_id": "mixed", "input": history.carrierInput()},
		map[string]any{"type": "custom_tool_call_output", "call_id": "mixed", "output": string(mustMarshalJSON(result))},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := transform.proxy.reconcileInputPrefix(&request, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	var replay []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &replay); err != nil || jsonString(replay[0], "input") != source {
		t.Fatalf("replay = %s, %v", request.fields["input"], err)
	}
}

func TestHpatchInlineExecution(t *testing.T) {
	t.Parallel()
	transform, overrides := mixedTestTransform(t)
	source := "shell printf 'draft\\n' > notes.txt\n" +
		"in notes.txt\ntype \"draft\" \"ready\"\n" +
		"shell cat notes.txt | tr a-z A-Z\n" +
		"shell <<SHELL\nprintf 'block\\n'\nSHELL\n" +
		"shell printf '%s' 'inline'\n" +
		"shell printf '%s' 'a\rb'"
	history, err := transform.translate("inline", source, nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate = %v, %s", err, history.TranslationError)
	}
	var result mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, history.carrierInput(), &result, overrides)
	if result.Sequence.Started != 6 || result.Sequence.Stopped != "" ||
		result.Results[2].Output != "READY\n" || result.Results[3].Output != "block\n" ||
		result.Results[4].Output != "inline" || result.Results[5].Output != "a\rb" {
		t.Fatalf("result = %+v", result)
	}
}

func TestHpatchInlineStopsOnFailure(t *testing.T) {
	t.Parallel()
	transform, overrides := mixedTestTransform(t)
	history, err := transform.translate("inline-failure", "shell printf failed; exit 7\nnew unstarted.txt\ntype \"no\"", nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate = %v, %s", err, history.TranslationError)
	}
	var result mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, history.carrierInput(), &result, overrides)
	if result.Sequence.Started != 1 || result.Sequence.NotStarted != 1 || result.Sequence.Stopped != "nonzero_exit" ||
		result.Results[0].Output != "failed" || result.Results[0].ExitCode != 7 {
		t.Fatalf("result = %+v", result)
	}
}

func TestHpatchInlineRejectsDoubleLessBeforeEffects(t *testing.T) {
	transform, _ := mixedTestTransform(t)
	for index, command := range []string{"echo '<<'", "cat <<<text"} {
		source := "shell touch never\nshell " + command + "\nnew never.txt\ntype \"data\""
		history, err := transform.translate(fmt.Sprintf("inline-double-less-%d", index), source, nil)
		if err != nil || !strings.Contains(history.TranslationError, "<< is not allowed") ||
			strings.Contains(history.carrierInput(), "tools.") {
			t.Fatalf("inline %q = %+v, %v", command, history, err)
		}
	}
}

func TestHpatchInlineCannotFallbackFromUnclosedBlock(t *testing.T) {
	transform, _ := mixedTestTransform(t)
	history, err := transform.translate("unclosed", "shell <<SHELL\nnew never.txt\ntype \"data\"", nil)
	if err != nil || !strings.Contains(history.TranslationError, "unterminated heredoc") ||
		strings.Contains(history.carrierInput(), "tools.") {
		t.Fatalf("unclosed frame = %+v, %v", history, err)
	}
}

func TestHpatchMixedLargeSourceUsesStdin(t *testing.T) {
	t.Parallel()
	transform, overrides := mixedTestTransform(t)
	content := strings.Repeat("x", 600000) + "世界\n"
	source := "shell <<SHELL\ntrue\nSHELL\nnew large.txt\ntype " +
		string(mustMarshalJSON(content)) + "\nshell <<SHELL\nwc -c < large.txt\nSHELL"
	history, err := transform.translate("large", source, nil)
	if err != nil || history.TranslationError != "" || !strings.Contains(history.carrierInput(), "await translateSource(") {
		t.Fatalf("translate = %v, %s", err, history.TranslationError)
	}
	overrides += `const ordinaryExec = tools.exec_command;
tools.exec_command = async args => {
  if (Buffer.byteLength(args.cmd) > 65536 || args.cmd.includes('--hpatch-')) throw new Error('private transport in command');
  return ordinaryExec(args);
};`
	var result mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, history.carrierInput(), &result, overrides)
	if result.Sequence.Started != 3 || result.Sequence.Stopped != "" {
		t.Fatalf("result = %+v", result)
	}
	actual, err := os.ReadFile(filepath.Join(transform.directory, "large.txt"))
	if err != nil || string(actual) != content {
		t.Fatalf("large content: got %d bytes, err=%v", len(actual), err)
	}
}

func TestHpatchMixedPreflight(t *testing.T) {
	for name, source := range map[string]string{
		"edit syntax":   "shell <<SHELL\ntouch unexpected\nSHELL\nnew a\nbogus",
		"shell header":  "new a\ntype \"ready\"\nshell <<SHELL\n#!params={\"cmd\":\"bad\"}\ntrue\nSHELL",
		"missing close": "new a\ntype \"ready\"\nshell <<SHELL\ntrue",
	} {
		t.Run(name, func(t *testing.T) {
			transform, _ := mixedTestTransform(t)
			history, err := transform.translate("rejected", source, nil)
			if err != nil || history.TranslationError == "" || strings.Contains(history.carrierInput(), "tools.") {
				t.Fatalf("preflight = %+v, %v", history, err)
			}
		})
	}
	t.Run("native-only", func(t *testing.T) {
		transform, _ := mixedTestTransform(t)
		transform.nativeTools = true
		history, err := transform.translate("native", "shell <<SHELL\ntrue\nSHELL", nil)
		if err != nil || !strings.Contains(history.TranslationError, "requires Code Mode") {
			t.Fatalf("native = %+v, %v", history, err)
		}
	})
}

func TestHpatchMixedStopsWithoutRollback(t *testing.T) {
	t.Parallel()
	for name, middle := range map[string]string{
		"nonzero_exit":  "shell <<SHELL\nprintf failed; exit 7\nSHELL",
		"edit_rejected": "in missing\ntype \"draft\" \"ready\"",
	} {
		t.Run(name, func(t *testing.T) {
			transform, overrides := mixedTestTransform(t)
			source := "new kept.txt\ntype \"kept\\n\"\n" +
				"shell <<SHELL\ntrue\nSHELL\n" + middle +
				"\nshell <<SHELL\ntouch unexpected\nSHELL"
			history, err := transform.translate("failure", source, nil)
			if err != nil || history.TranslationError != "" {
				t.Fatalf("translate = %v, %s", err, history.TranslationError)
			}
			var result mixedScriptResult
			runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, history.carrierInput(), &result, overrides)
			if result.Sequence.Stopped != name || result.Sequence.Started != 3 || result.Sequence.NotStarted != 1 {
				t.Fatalf("result = %+v", result)
			}
			if content, err := os.ReadFile(filepath.Join(transform.directory, "kept.txt")); err != nil || string(content) != "kept\n" {
				t.Fatalf("completed edit lost: %q, %v", content, err)
			}
			if _, err := os.Stat(filepath.Join(transform.directory, "unexpected")); !os.IsNotExist(err) {
				t.Fatal("later shell ran")
			}
		})
	}
}

func TestHpatchMixedWaitsForSessions(t *testing.T) {
	t.Parallel()
	transform, overrides := mixedTestTransform(t)
	history, err := transform.translate("wait", "shell <<SHELL\nfirst\nSHELL\nshell <<SHELL\nsecond\nSHELL", nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate = %v, %s", err, history.TranslationError)
	}
	overrides += `let calls = 0;
let pending = true;
tools.exec_command = async () => {
  if (++calls === 1) return {session_id: 42, output: 'start'};
  if (pending) throw new Error('started before preceding session finished');
  return {exit_code: 0, output: 'second'};
};
tools.write_stdin = async args => {
  if (args.session_id !== 42 || args.chars !== '' || args.yield_time_ms !== 300000) throw new Error('invalid continuation');
  pending = false;
  return {exit_code: 0, output: 'end'};
};`
	var result mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, history.carrierInput(), &result, overrides)
	if result.Results[0].Output != "startend" || result.Results[1].Output != "second" || result.Sequence.Started != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func TestHpatchMixedHostFailuresKeepPartialResults(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"patch", "continuation", "truncated"} {
		t.Run(stage, func(t *testing.T) {
			transform, overrides := mixedTestTransform(t)
			source := "shell <<SHELL\nfirst\nSHELL\nnew file.txt\ntype \"ready\\n\"\nshell <<SHELL\nnever\nSHELL"
			history, err := transform.translate("host-error", source, nil)
			if err != nil || history.TranslationError != "" {
				t.Fatalf("translate = %v, %s", err, history.TranslationError)
			}
			overrides += `let calls = 0;
tools.exec_command = async () => {
  calls++;
  if (calls === 1) return {exit_code: 0, output: 'completed'};
  return {exit_code: 0, output: JSON.stringify({patch: 'patch', report: 'report', diagnostic: ''})};
};
tools.apply_patch = async () => { throw new Error('host refused'); };
tools.write_stdin = async () => { throw new Error('host cancelled'); };
`
			if stage == "continuation" {
				overrides += "tools.exec_command = async () => ({session_id: 42, output: 'partial'});\n"
			}
			if stage == "truncated" {
				overrides += "tools.exec_command = async () => ({exit_code: 0, output: '{truncated'});\ntools.apply_patch = async () => { throw new Error('must not apply'); };\n"
			}
			// Observe the production finally projection while consuming the
			// intentionally propagated host error in this test wrapper only.
			carrier := "try {\n" + history.carrierInput() + "\n} catch {}\n"
			var result mixedScriptResult
			runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, carrier, &result, overrides)
			if result.Sequence.Stopped != "host_error" || result.Sequence.NotStarted < 1 {
				t.Fatalf("result = %+v", result)
			}
			if stage == "continuation" && (result.Results[0].SessionID != 42 || result.Results[0].Output != "partial") {
				t.Fatalf("lost pending handle: %+v", result)
			}
			if stage == "patch" && (result.Results[0].Status != "completed" || result.Results[1].Status != "interrupted") {
				t.Fatalf("lost completed prefix: %+v", result)
			}
		})
	}
}

func TestHpatchTranslationWorkerDoesNotApply(t *testing.T) {
	directory := t.TempDir()
	var stdout bytes.Buffer
	if err := runHpatchTranslation(t.Context(), directory, "new file.txt\ntype \"ready\\n\"", &stdout); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || !strings.Contains(result.Patch, "*** Add File:") {
		t.Fatalf("translation = %s, %v", &stdout, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "file.txt")); !os.IsNotExist(err) {
		t.Fatal("translation worker applied the patch")
	}
	ctx := t.Context()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	stdout.Reset()
	if err := runHpatchTranslation(cancelled, directory, "new file.txt", &stdout); err == nil || stdout.Len() != 0 {
		t.Fatalf("cancelled worker published output: %s, %v", &stdout, err)
	}
}

func TestHpatchMixedCheckpoints(t *testing.T) {
	t.Parallel()
	transform, overrides := mixedTestTransform(t)
	history, err := transform.translate("checkpoints", "shell true\nnew checkpoint.txt\ntype \"done\\n\"\nshell false", nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate = %v, %s", err, history.TranslationError)
	}
	overrides += `
const notifications = [];
globalThis.notify = value => { notifications.push(value); };
const originalExec = tools.exec_command;
let yielded = false;
tools.exec_command = async args => {
  if (!yielded && args.cmd !== 'shell') { yielded = true; return {session_id: 42, output: 'start'}; }
  return originalExec(args);
};
tools.write_stdin = async args => {
  if (args.session_id !== 42) throw new Error('wrong session');
  return {exit_code: 0, output: 'finished'};
};
`
	// Observe acknowledged durable state, not model-visible notifications.
	path := filepath.Join(transform.shellDirectory, "mixed-"+hpatchRecoveryFor(history).Handle)
	carrier := `
const checkpoints = [];
const checkpointWrite = tools.write_stdin;
tools.write_stdin = async args => {
  const result = await checkpointWrite(args);
  if (args.chars?.startsWith('{"operation":"checkpoint"')) {
    const saved = JSON.parse(fs.readFileSync(` + string(mustMarshalJSON(path)) + `, 'utf8')).progress;
    checkpoints.push({...saved.current, completed_segments: saved.results.length +
      (saved.current?.status === 'completed' ? 1 : 0)});
  }
  return result;
};
` + history.carrierInput() + `
if (notifications.length !== 0) throw new Error('checkpoint notification overhead');
if (!checkpoints.some(c => c.phase === 'session_available' && c.session_id === 42)) throw new Error('session handle not published');
if (!checkpoints.some(c => c.phase === 'awaiting_session' && c.session_id === 42)) throw new Error('wait lost its handle');
const applying = checkpoints.findIndex(c => c.segment === 2 && c.phase === 'applying');
const completed = checkpoints.findIndex(c => c.segment === 2 && c.phase === 'segment_completed');
if (applying < 0 || completed <= applying) throw new Error('missing application checkpoints');
const lastCheckpoint = checkpoints.at(-1);
if (lastCheckpoint.phase !== 'segment_stopped' || lastCheckpoint.completed_segments !== 2) throw new Error('incorrect completed prefix');
`
	var result mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, carrier, &result, overrides)
	if result.Sequence.Stopped != "nonzero_exit" || result.Sequence.Started != 3 {
		t.Fatalf("result = %+v", result)
	}
}

func TestHpatchRejectedResumeRecoveryKeepsOriginalContinuation(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%v", native), func(t *testing.T) {
			transform, _ := mixedTestTransform(t)
			state, err := transform.retainMixedScript("", "", "shell true", []hpatchResumeSegment{
				{Source: "true", Line: 1, Kind: "shell"},
			})
			if err != nil {
				t.Fatal(err)
			}
			transform.nativeTools = native
			history, err := transform.translate("rejected-resume", "resume "+state.Handle+" retry\nshell <<SHELL\ntrue", nil)
			if err != nil || history.TranslationError == "" {
				t.Fatalf("resume was not rejected: %v, %+v", err, history)
			}
			_, err = recoveryHistoryOf(slices.Values([]mekugiHistory{durableHistory(history)}))
			if err == nil || !strings.Contains(err.Error(), "original continuation handle") ||
				strings.Contains(err.Error(), "no segment ran") || strings.Contains(err.Error(), "resume HANDLE") {
				t.Fatalf("rejected resume lost original continuation: %v", err)
			}
		})
	}
}

func TestHpatchNativePreflightRecoveryIsRetained(t *testing.T) {
	transform, _ := mixedTestTransform(t)
	transform.nativeTools = true
	history, err := transform.translate("native-preflight", "shell true", nil)
	if err != nil || history.TranslationError == "" {
		t.Fatalf("native preflight was not rejected: %v, %+v", err, history)
	}
	base, err := recoveryHistoryOf(slices.Values([]mekugiHistory{durableHistory(history)}))
	if err != nil || base.recoveryBaseline() != history.Script ||
		strings.Contains(history.TranslationError, "resume HANDLE") {
		t.Fatalf("native preflight was not retained for text recovery: %v, %+v", err, history)
	}
}

func TestHpatchMixedPreflightRecoveryExecutesCorrectedScriptOnce(t *testing.T) {
	transform, overrides := mixedTestTransform(t)
	const source = "shell printf x >> attempts\nnew result.txt\ntype \"done\\n\"\nontail"
	rejected, err := transform.translate("mixed-preflight", source, nil)
	if err != nil || rejected.TranslationError == "" || strings.Contains(rejected.carrierInput(), "tools.") ||
		!strings.Contains(rejected.TranslationError, "Invalid final input at script line 4; no effects were applied.") ||
		!strings.Contains(rejected.TranslationError, mekugi.TextReferences(source, 4)) {
		t.Fatalf("preflight rejection = %v, %+v", err, rejected)
	}
	if _, err := os.Stat(filepath.Join(transform.directory, "attempts")); !os.IsNotExist(err) {
		t.Fatal("rejected preflight executed its shell")
	}

	transform.visible = map[string]mekugiHistory{"mixed-preflight": durableHistory(rejected)}
	clear(transform.local)

	row := strings.Fields(mekugi.TextReferences(source, 4))[0]
	recovered, err := transform.translateRecovery("mixed-recovery", "type "+row+` ""`, nil)
	if err != nil || recovered.TranslationError != "" || recovered.CarrierPayload == "" ||
		recovered.ToolName != mekugiRecoveryToolName || recovered.Attempt != 2 ||
		hpatchRecoveryFor(recovered) == nil {
		t.Fatalf("recovery = %v, %+v", err, recovered)
	}
	var result mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, recovered.carrierInput(), &result, overrides)
	if result.Sequence.Started != 2 || result.Sequence.Stopped != "" {
		t.Fatalf("corrected sequence = %+v", result)
	}
	attempts, err := os.ReadFile(filepath.Join(transform.directory, "attempts"))
	if err != nil || string(attempts) != "x" {
		t.Fatalf("shell executions = %q, %v", attempts, err)
	}
	content, err := os.ReadFile(filepath.Join(transform.directory, "result.txt"))
	if err != nil || string(content) != "done\n" {
		t.Fatalf("corrected edit = %q, %v", content, err)
	}
}

func TestHpatchEmptyShellProgramsCompleteWithoutExecution(t *testing.T) {
	t.Parallel()
	transform, overrides := mixedTestTransform(t)
	source := "new before.txt\ntype \"before\"\nshell  \nshell \t\nshell \t \nshell <<SHELL\nSHELL\nshell <<SHELL\n \nSHELL\nnew after.txt\ntype \"after\""
	history, err := transform.translate("empty-shells", source, nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("empty shells rejected submission: %v, %s", err, history.TranslationError)
	}
	var result mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, history.carrierInput(), &result, overrides)
	if result.Sequence.Stopped != "" || result.Sequence.Started != 7 || len(result.Results) != 7 {
		t.Fatalf("empty shell sequence = %+v", result)
	}
	for _, segment := range result.Results[1:6] {
		if segment.Kind != "shell" || segment.Status != "completed" || segment.ExitCode != 0 || segment.Output != "" || segment.SessionID != 0 {
			t.Fatalf("empty shell did not complete as a no-op: %+v", segment)
		}
	}
	for _, name := range []string{"before", "after"} {
		content, err := os.ReadFile(filepath.Join(transform.directory, name+".txt"))
		if err != nil || string(content) != name+"\n" {
			t.Fatalf("surrounding edit %s = %q, %v", name, content, err)
		}
	}
}

func TestHpatchMixedRecoverySelectsOnlyPreflightFailures(t *testing.T) {
	for _, input := range []string{
		"shell true\nnew result\ntype \"done\"",
		"shell <<SHELL\ntrue",
		"shell ",
		"shell <<SHELL\nSHELL",
		"shell <<SHELL\n \nSHELL",
	} {
		transform, _ := mixedTestTransform(t)
		history, err := transform.translate("mixed-recovery", input, nil)
		if err != nil {
			t.Fatal(err)
		}
		older := mekugiHistory{
			ToolName: mekugiToolName, EvaluatorRejected: true,
			TranslationError: "older rejection", sequence: 0,
		}
		base, recoveryErr := recoveryHistoryOf(slices.Values([]mekugiHistory{older, history}))
		if history.TranslationError != "" {
			if recoveryErr != nil || base.recoveryBaseline() != history.recoveryBaseline() ||
				strings.Contains(history.TranslationError, "resume HANDLE") {
				t.Fatalf("preflight rejection was not selected safely: %v, %+v", recoveryErr, history)
			}
		} else if recoveryErr == nil || !strings.Contains(recoveryErr.Error(), "checkpoints") ||
			!strings.Contains(recoveryErr.Error(), "resume HANDLE") {
			t.Fatalf("retained work lost continuation guidance: %v", recoveryErr)
		}
	}
}

func TestHpatchResumeRepairsOnlyFailedSegment(t *testing.T) {
	t.Parallel()
	transform, overrides := mixedTestTransform(t)
	source := "new kept.txt\ntype \"kept\\n\"\nshell printf x >> attempts; test -f repaired\n" +
		"new suffix.txt\ntype \"pending\\n\"\nshell printf done"
	history, err := transform.translate("resumable", source, nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate = %v, %s", err, history.TranslationError)
	}
	var failed mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, history.carrierInput(), &failed, overrides)
	if failed.ResumeHandle == "" || failed.Sequence.Stopped != "nonzero_exit" {
		t.Fatalf("failed = %+v", failed)
	}
	// A retry may repeat the failed shell's effects only after explicit inspection.
	// The completed new-file segment must not run again.
	resume, err := transform.translate("resume", "resume "+failed.ResumeHandle+" retry\nshell printf repaired", nil)
	if err != nil || resume.TranslationError != "" {
		t.Fatalf("resume = %v, %s", err, resume.TranslationError)
	}
	var completed mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, resume.carrierInput(), &completed, overrides)
	if completed.Sequence.Started != 4 || completed.Sequence.Stopped != "" ||
		completed.Results[1].Output != "repaired" || completed.Results[3].Output != "done" {
		t.Fatalf("completed = %+v", completed)
	}
	attempts, err := os.ReadFile(filepath.Join(transform.directory, "attempts"))
	if err != nil || string(attempts) != "x" {
		t.Fatalf("failed shell was replayed: %q, %v", attempts, err)
	}
	again, err := transform.translate("resume-completed", "resume "+failed.ResumeHandle, nil)
	if err != nil || again.TranslationError != "" {
		t.Fatalf("completed resume = %v, %s", err, again.TranslationError)
	}
	var repeated mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, again.carrierInput(), &repeated, overrides)
	if repeated.Sequence.Stopped != "" || len(repeated.Results) != 4 {
		t.Fatalf("completed work replayed: %+v", repeated)
	}
}

func TestHpatchResumeFreshTargetValidation(t *testing.T) {
	t.Parallel()
	transform, overrides := mixedTestTransform(t)
	if err := os.WriteFile(filepath.Join(transform.directory, "target.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	history, err := transform.translate("fresh", "shell false\nin target.txt\ntype \"old\" \"new\"", nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate = %v, %s", err, history.TranslationError)
	}
	var failed mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, history.carrierInput(), &failed, overrides)
	if err := os.WriteFile(filepath.Join(transform.directory, "target.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resume, err := transform.translate("fresh-resume", "resume "+failed.ResumeHandle+" retry\nshell true", nil)
	if err != nil || resume.TranslationError != "" {
		t.Fatalf("resume = %v, %s", err, resume.TranslationError)
	}
	var rejected mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, resume.carrierInput(), &rejected, overrides)
	if rejected.Sequence.Stopped != "edit_rejected" {
		t.Fatalf("stale suffix was applied: %+v", rejected)
	}
	repair, err := transform.translate("fresh-repair", "resume "+failed.ResumeHandle+" retry\nin target.txt\ntype \"changed\" \"new\"", nil)
	if err != nil || repair.TranslationError != "" {
		t.Fatalf("repair = %v, %s", err, repair.TranslationError)
	}
	var completed mixedScriptResult
	runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, repair.carrierInput(), &completed, overrides)
	actual, err := os.ReadFile(filepath.Join(transform.directory, "target.txt"))
	if err != nil || string(actual) != "new\n" || completed.Sequence.Stopped != "" {
		t.Fatalf("repair = %+v; content %q, %v", completed, actual, err)
	}
}

func TestHpatchResumeRejectsUnavailableHandles(t *testing.T) {
	transform, _ := mixedTestTransform(t)
	for index, input := range []string{
		"resume missing",
		"resume M00000000000000000000000000000000",
		"resume M00000000000000000000000000000000 force",
		"resume M../../outside",
	} {
		history, err := transform.translate(fmt.Sprintf("invalid-resume-%d", index), input, nil)
		if err != nil || history.TranslationError == "" || history.CarrierPayload != "text("+strconv.Quote(history.TranslationError)+");" {
			t.Fatalf("invalid handle emitted execution: %+v, %v", history, err)
		}
	}
}

func TestHpatchRepairAndResume(t *testing.T) {
	t.Parallel()
	for _, rejectedRepair := range []bool{false, true} {
		t.Run(fmt.Sprint(rejectedRepair), func(t *testing.T) {
			t.Parallel()
			transform, overrides := mixedTestTransform(t)
			run := func(call, source string) mixedScriptResult {
				t.Helper()
				history, err := transform.translate(call, source, nil)
				if err != nil || history.TranslationError != "" {
					t.Fatalf("translate: %v, %s", err, history.TranslationError)
				}
				var result mixedScriptResult
				runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory,
					history.carrierInput(), &result, overrides)
				return result
			}
			failed := run("original", "new target.txt\ntype \"broken\\n\"\nshell printf x >> attempts; test \"$(cat target.txt)\" = fixed\nshell printf x >> suffix")
			if failed.Sequence.Stopped != "nonzero_exit" {
				t.Fatalf("expected test failure: %+v", failed)
			}
			target := "broken"
			if rejectedRepair {
				target = "missing"
			}
			result := run("repair", "resume "+failed.ResumeHandle+" repair\nin target.txt\ntype \""+target+"\" \"fixed\"")
			if rejectedRepair {
				if result.Sequence.Stopped != "edit_rejected" || !result.Results[1].Repair {
					t.Fatalf("repair rejection: %+v", result)
				}
				attempts, err := os.ReadFile(filepath.Join(transform.directory, "attempts"))
				if err != nil || string(attempts) != "x" {
					t.Fatalf("failed repair retried test: %q, %v", attempts, err)
				}
				result = run("repair-retry", "resume "+failed.ResumeHandle+" retry\nin target.txt\ntype \"broken\" \"fixed\"")
			}
			if result.ResumeHandle != failed.ResumeHandle || result.Sequence.Count != 4 ||
				result.Sequence.Stopped != "" || len(result.Results) != 4 || !result.Results[1].Repair {
				t.Fatalf("repair did not continue the retained suffix: %+v", result)
			}
			for name, want := range map[string]string{"attempts": "xx", "suffix": "x", "target.txt": "fixed\n"} {
				data, err := os.ReadFile(filepath.Join(transform.directory, name))
				if err != nil || string(data) != want {
					t.Fatalf("%s = %q, want %q, error %v", name, data, want, err)
				}
			}
			again := run("completed", "resume "+failed.ResumeHandle)
			if again.Sequence.Stopped != "" || len(again.Results) != 4 {
				t.Fatalf("completed resume: %+v", again)
			}
			data, err := os.ReadFile(filepath.Join(transform.directory, "attempts"))
			if err != nil || string(data) != "xx" {
				t.Fatalf("completed work replayed: %q, %v", data, err)
			}
		})
	}
}

func TestHpatchRepairPreservesReplacement(t *testing.T) {
	t.Parallel()
	transform, overrides := mixedTestTransform(t)
	run := func(call, source string) mixedScriptResult {
		t.Helper()
		history, err := transform.translate(call, source, nil)
		if err != nil || history.TranslationError != "" {
			t.Fatalf("translate: %v, %s", err, history.TranslationError)
		}
		var result mixedScriptResult
		runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory,
			history.carrierInput(), &result, overrides)
		return result
	}
	failed := run("original", "shell exit 17\nshell printf x >> suffix")
	failed = run("replace", "resume "+failed.ResumeHandle+" retry\nshell test -f repaired")
	completed := run("repair", "resume "+failed.ResumeHandle+" repair\nnew repaired\ntype \"ready\"")
	if completed.Sequence.Stopped != "" || completed.Sequence.Count != 3 {
		t.Fatalf("repair lost the previously replaced test: %+v", completed)
	}
}

func TestHpatchRepairPreflight(t *testing.T) {
	transform, _ := mixedTestTransform(t)
	state, err := transform.retainMixedScript("", "", "shell false", []hpatchResumeSegment{{Kind: "shell", Source: "false", Line: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for index, suffix := range []string{"", "\nshell true", "\nnew x\ntype \"x\"\nshell true", "\nnew x\ntype"} {
		history, err := transform.translate(fmt.Sprintf("bad-repair-%d", index), "resume "+state.Handle+" repair"+suffix, nil)
		if err != nil || history.TranslationError == "" || history.CarrierPayload != "text("+strconv.Quote(history.TranslationError)+");" {
			t.Fatalf("invalid repair emitted execution: %+v, %v", history, err)
		}
	}
}
