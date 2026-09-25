package router

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
	"mvdan.cc/sh/v3/syntax"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func observeTestCommand(t *testing.T, workdir, command string) *execObservation {
	t.Helper()
	observation, observed := captureExecObservation([]execCommandInput{{Command: command, Workdir: workdir, Shell: "bash"}}, false, false, execCaptureEnv{})
	if !observed {
		t.Fatalf("%q was not observed", command)
	}
	return observation
}

func reviewSummary(workspace string, files []mekugi.ReviewFile) []string {
	var summary []string
	for _, file := range files {
		text := file.Action().Title() + " " + strings.TrimPrefix(cmpOrPath(file.AfterPath, file.BeforePath), workspace+"/")
		if file.Action() == mekugi.ReviewMove {
			text = "Move " + strings.TrimPrefix(file.BeforePath, workspace+"/") + " -> " + strings.TrimPrefix(file.AfterPath, workspace+"/")
		}
		if file.CopyFrom != "" {
			text += " (copy of " + strings.TrimPrefix(file.CopyFrom, workspace+"/") + ")"
		}
		if file.Binary {
			text += " binary"
		}
		if file.Incomplete != "" {
			text += " incomplete"
		}
		summary = append(summary, text)
	}
	slices.Sort(summary)
	return summary
}

func cmpOrPath(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func TestExecObservationReconcilesDeclaredEffects(t *testing.T) {
	for _, test := range []struct {
		name    string
		setup   map[string]string
		command string
		run     func(t *testing.T, workspace string)
		want    []string
	}{
		{
			name: "copy", setup: map[string]string{"a.txt": "a\n"}, command: "cp a.txt b.txt",
			run:  func(t *testing.T, w string) { writeTestFile(t, filepath.Join(w, "b.txt"), "a\n") },
			want: []string{"Create b.txt (copy of a.txt)"},
		},
		{
			name: "copy into directory", setup: map[string]string{"a.txt": "a\n", "dir/keep": ""}, command: "cp a.txt dir",
			run:  func(t *testing.T, w string) { writeTestFile(t, filepath.Join(w, "dir/a.txt"), "a\n") },
			want: []string{"Create dir/a.txt (copy of a.txt)"},
		},
		{
			name: "move", setup: map[string]string{"old.go": "package x\n"}, command: "mv old.go new.go",
			run: func(t *testing.T, w string) {
				if err := os.Rename(filepath.Join(w, "old.go"), filepath.Join(w, "new.go")); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"Move old.go -> new.go"},
		},
		{
			name: "glob delete", setup: map[string]string{"a.tmp": "1\n", "b.tmp": "2\n", "c.txt": "3\n"}, command: "rm *.tmp",
			run: func(t *testing.T, w string) {
				_ = os.Remove(filepath.Join(w, "a.tmp"))
				_ = os.Remove(filepath.Join(w, "b.tmp"))
			},
			want: []string{"Delete a.tmp", "Delete b.tmp"},
		},
		{
			name: "recursive delete", setup: map[string]string{"build/x.o": "x", "build/sub/y.o": "y"}, command: "rm -rf build",
			run:  func(t *testing.T, w string) { _ = os.RemoveAll(filepath.Join(w, "build")) },
			want: []string{"Delete build/sub/y.o", "Delete build/x.o"},
		},
		{
			name: "redirect", command: "cd sub && printf 'x\\n' > out.txt",
			setup: map[string]string{"sub/.keep": ""},
			run:   func(t *testing.T, w string) { writeTestFile(t, filepath.Join(w, "sub/out.txt"), "x\n") },
			want:  []string{"Create sub/out.txt"},
		},
		{
			name: "binary", setup: map[string]string{"blob": "\xff\x00"}, command: "truncate -s 0 blob",
			run:  func(t *testing.T, w string) { writeTestFile(t, filepath.Join(w, "blob"), "\xfe\x01") },
			want: []string{"Edit blob binary"},
		},
		{
			name: "symlink", command: "ln -s target link",
			run: func(t *testing.T, w string) {
				if err := os.Symlink("target", filepath.Join(w, "link")); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"Create link"},
		},
		{
			name: "unchanged", setup: map[string]string{"a": "same\n"}, command: "touch a",
			run: func(*testing.T, string) {},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			for path, content := range test.setup {
				writeTestFile(t, filepath.Join(workspace, path), content)
			}
			observation := observeTestCommand(t, workspace, test.command)
			test.run(t, workspace)
			reviews, complete, _, _ := reconcileExecObservation(*observation, execReconcileEnv{})
			if !complete {
				t.Fatalf("incomplete evidence: %+v", reviews)
			}
			if got := reviewSummary(workspace, reviews); !slices.Equal(got, test.want) {
				t.Fatalf("reviews = %q, want %q", got, test.want)
			}
		})
	}
}

// Running the commands checks capture against real shell semantics, where an
// earlier statement decides what a later destination names.
func TestExecObservationFollowsShellSemantics(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	for _, test := range []struct {
		name    string
		setup   map[string]string
		links   map[string]string
		command string
		want    []string
		partial bool
	}{
		{name: "move into a new directory", setup: map[string]string{"a.txt": "a\n"}, command: "mkdir old && mv a.txt old", want: []string{"Move a.txt -> old/a.txt"}},
		{name: "copy into a new directory", setup: map[string]string{"app": "bin\n"}, command: "mkdir -p dist && cp app dist", want: []string{"Create dist/app (copy of app)"}},
		{
			name: "replace a directory", setup: map[string]string{"build/new.js": "new\n", "dist/old.js": "old\n"},
			command: "rm -rf dist && cp -r build dist", want: []string{"Create dist/new.js (copy of build/new.js)", "Delete dist/old.js"},
		},
		{
			name: "copy a tree created by the command", setup: map[string]string{"keep": ""},
			command: "mkdir src && printf 'x\\n' > src/new && cp -r src dst", want: []string{"Create dst/new", "Create src/new"},
		},
		{name: "glob over a created file", setup: map[string]string{"a.txt": "a\n", "dir/.keep": ""}, command: "printf 'b\\n' > b.txt && mv *.txt dir", want: []string{"Create dir/b.txt", "Move a.txt -> dir/a.txt"}},
		{name: "write through a symlink", setup: map[string]string{"real": "old\n"}, links: map[string]string{"link": "real"}, command: "printf 'new\\n' > link", want: []string{"Edit real"}},
		{name: "dangling symlink", links: map[string]string{"link": "missing"}, command: "printf 'new\\n' > link", want: []string{"Edit link incomplete"}, partial: true},
		{name: "empty files are not moves", setup: map[string]string{"empty": ""}, command: "rm empty && touch other", want: []string{"Create other", "Delete empty"}},
		{name: "NUL content is binary", setup: map[string]string{"data": "a\x00b"}, command: "printf 'c' >> data", want: []string{"Edit data binary"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			for path, content := range test.setup {
				writeTestFile(t, filepath.Join(workspace, path), content)
			}
			for link, target := range test.links {
				if err := os.Symlink(target, filepath.Join(workspace, link)); err != nil {
					t.Fatal(err)
				}
			}
			observation := observeTestCommand(t, workspace, test.command)
			command := exec.Command(bash, "-c", test.command)
			command.Dir = workspace
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("%s: %v\n%s", test.command, err, output)
			}
			reviews, complete, _, _ := reconcileExecObservation(*observation, execReconcileEnv{})
			if complete == test.partial {
				t.Fatalf("complete = %v: %+v", complete, reviews)
			}
			if got := reviewSummary(workspace, reviews); !slices.Equal(got, test.want) {
				t.Fatalf("reviews = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExecObservationBoundsEncodedCapture(t *testing.T) {
	workspace := t.TempDir()
	// JSON escapes control bytes as six bytes, so this content encodes past
	// the bound.
	writeTestFile(t, filepath.Join(workspace, "page.html"), strings.Repeat("\x01", 2<<20))
	writeTestFile(t, filepath.Join(workspace, "small"), "kept\n")
	observation := observeTestCommand(t, workspace, "touch page.html small")
	if size := len(mustMarshalJSON(observation)); size > maxExecObservationBytes {
		t.Fatalf("encoded capture is %d bytes", size)
	}
	for _, file := range observation.Files {
		if strings.HasSuffix(file.Path, "page.html") && (file.Content != "" || file.Stamp == "") {
			t.Fatalf("large file kept its content: %d bytes", len(file.Content))
		}
		if strings.HasSuffix(file.Path, "small") && file.Content != "kept\n" {
			t.Fatalf("small file lost its content: %+v", file)
		}
	}
	if reviews, complete, _, _ := reconcileExecObservation(*observation, execReconcileEnv{}); !complete || len(reviews) != 0 {
		t.Fatalf("unchanged files were reported: %+v", reviews)
	}
}

func TestRequestSessionShell(t *testing.T) {
	context := func(body string) map[string]any {
		return map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "<environment_context>\n" + body + "\n</environment_context>"}}}
	}
	for _, test := range []struct {
		name  string
		input []any
		want  string
	}{
		{"single", []any{context("  <shell>bash</shell>"), map[string]any{"role": "user", "content": "task"}}, "bash"},
		{"update keeps the shell", []any{context("  <shell>zsh</shell>"), context("  <current_date>2026-09-24</current_date>")}, "zsh"},
		{"latest wins", []any{context("  <shell>zsh</shell>"), context("  <shell>bash</shell>")}, "bash"},
		{"environments disagree", []any{context("  <environments>\n    <shell>bash</shell>\n    <shell>powershell</shell>\n  </environments>")}, ""},
		{"quoted in a message", []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "see <shell>bash</shell>"}}}}, ""},
	} {
		if got := requestSessionShell(mustTestJSON(t, test.input)); got != test.want {
			t.Errorf("%s: shell = %q, want %q", test.name, got, test.want)
		}
	}
	if _, ok := execCommandArguments(`{"cmd":"rm a","environment_id":"remote"}`, "/work", "bash"); ok {
		t.Error("a command for another environment was observed")
	}
	if command, ok := execCommandArguments(`{"cmd":"rm a"}`, "/work", "bash"); !ok || command.Shell != "bash" {
		t.Errorf("session shell was not applied: %+v", command)
	}
}

func TestExecObservationSymlinkReviewsLinkTarget(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "target"), "secret content\n")
	observation := observeTestCommand(t, workspace, "ln -s target link")
	if err := os.Symlink("target", filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	reviews, _, _, _ := reconcileExecObservation(*observation, execReconcileEnv{})
	if len(reviews) != 1 || !strings.Contains(reviews[0].Diff, "+-> target") || strings.Contains(reviews[0].Diff, "secret") {
		t.Fatalf("symlink review = %+v", reviews)
	}
}

func TestExecObservationBoundsCapture(t *testing.T) {
	workspace := t.TempDir()
	for index := range maxExecCaptureFiles + 4 {
		writeTestFile(t, filepath.Join(workspace, "many", strings.Repeat("f", 1+index/26)+string(rune('a'+index%26))), "x")
	}
	observation := observeTestCommand(t, workspace, "rm -rf many")
	if len(observation.Files) != maxExecCaptureFiles || len(observation.Omitted) == 0 {
		t.Fatalf("captured %d files, omitted %+v", len(observation.Files), observation.Omitted)
	}
	reviews, complete, _, _ := reconcileExecObservation(*observation, execReconcileEnv{})
	if complete || !slices.ContainsFunc(reviews, func(file mekugi.ReviewFile) bool { return file.Incomplete != "" }) {
		t.Fatal("an overflowing capture was reported as complete")
	}

	large := filepath.Join(workspace, "large")
	writeTestFile(t, large, strings.Repeat("x", maxNativePatchFileBytes+1))
	observation = observeTestCommand(t, workspace, "touch large")
	if reviews, complete, _, _ := reconcileExecObservation(*observation, execReconcileEnv{}); !complete || len(reviews) != 0 {
		t.Fatalf("an unchanged oversized file was reported: %+v", reviews)
	}
	writeTestFile(t, large, strings.Repeat("y", maxNativePatchFileBytes+2))
	if reviews, complete, _, _ := reconcileExecObservation(*observation, execReconcileEnv{}); complete || len(reviews) != 1 || reviews[0].Incomplete == "" {
		t.Fatalf("a changed oversized file = %+v, complete=%v", reviews, complete)
	}
}

func TestExecObservationSkipsNeutralCommands(t *testing.T) {
	for _, command := range []string{"ls", "git status"} {
		if _, observed := captureExecObservation([]execCommandInput{{Command: command, Workdir: t.TempDir(), Shell: "bash"}}, false, false, execCaptureEnv{}); observed {
			t.Errorf("%q was observed without a declared scope", command)
		}
	}
	if _, observed := captureExecObservation([]execCommandInput{{Command: "rm a", Workdir: t.TempDir(), Shell: "bash"}}, true, true, execCaptureEnv{}); !observed {
		t.Error("a dynamic Code Mode cell was not observed")
	}
}

func TestStockLiteralExecCommands(t *testing.T) {
	directory := "/work"
	commands, dynamic := stockLiteralExecCommands(`await tools.exec_command({cmd: "rm a", workdir: "sub"}); text(await tools.exec_command({cmd: 'cp b c'}));`, directory, "bash")
	if dynamic || len(commands) != 2 || commands[0].Command != "rm a" || commands[0].Workdir != "/work/sub" || commands[1].Workdir != directory {
		t.Fatalf("literal commands = %+v dynamic=%v", commands, dynamic)
	}
	for _, source := range []string{
		`const cmd = "rm a"; await tools.exec_command({cmd});`,
		`await tools.exec_command({cmd: name});`,
		`const run = tools.exec_command; await run({cmd: "rm a"});`,
		`const t = tools; await t.exec_command({cmd: "rm a"});`,
		`await tools["exec_command"]({cmd: "rm a"});`,
		`await tools.exec_command({cmd: "cat", tty: true}); await tools.write_stdin({session_id: 1, chars: "x"});`,
		`await tools?.exec_command({cmd: "rm a"});`,
		`await globalThis.tools.exec_command({cmd: "rm a"});`,
		`await eval("tools").exec_command({cmd: "rm a"});`,
		`await tools.exec_command({cmd: "rm a", environment_id: "remote"});`,
	} {
		if _, dynamic := stockLiteralExecCommands(source, directory, "bash"); !dynamic {
			t.Errorf("%s was treated as literal", source)
		}
	}
	for _, source := range []string{
		`await tools.write_stdin({session_id: 1});`,
		`await tools.write_stdin({session_id: 1, chars: ""});`,
		`const session_id = 1; await tools.write_stdin({session_id, chars: ""});`,
	} {
		if commands, dynamic := stockLiteralExecCommands(source, directory, "bash"); dynamic || len(commands) != 0 {
			t.Errorf("poll-only write_stdin call %s = %+v dynamic=%v; want no command and static", source, commands, dynamic)
		}
	}
	for _, source := range []string{
		`await tools.write_stdin({session_id: 1, chars: "x"});`,
		`await tools.write_stdin({session_id: 1, chars: input});`,
		`await tools.write_stdin({session_id: 1, ...options, chars: ""});`,
		`await tools.write_stdin({session_id: 1, ["chars"]: ""});`,
		`await tools.write_stdin({session_id: 1, chars: "", __proto__: {}});`,
		`await tools.write_stdin({session_id: 1, chars: "", chars: ""});`,
	} {
		if _, dynamic := stockLiteralExecCommands(source, directory, "bash"); !dynamic {
			t.Errorf("ambiguous write_stdin call %s was treated as a poll", source)
		}
	}
}

func TestCodeModeExecLiteralCommandAndResultDependentPollStayDirect(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "target.txt")
	writeTestFile(t, target, "before\n")
	writeTestFile(t, filepath.Join(workspace, "edit.py"), "from pathlib import Path\nPath('target.txt').write_text('after\\n')\n")
	source := `const r = await tools.exec_command({cmd: "python3 edit.py"}); await tools.write_stdin({session_id: r.session_id, chars: ""});`
	commands, dynamic := stockLiteralExecCommands(source, workspace, "bash")
	if dynamic || len(commands) != 1 || commands[0].Command != "python3 edit.py" {
		t.Fatalf("same-cell command and polling = %+v dynamic=%v", commands, dynamic)
	}
	observation, observed := captureExecObservation(commands, dynamic, true, execCaptureEnv{directory: workspace})
	if !observed || observation == nil || observation.Class == execOpaque.String() || !observation.CodeMode {
		t.Fatalf("Code Mode observation = %+v observed=%v; polling must not make the literal command opaque", observation, observed)
	}
	var baseline *execFileSnapshot
	for i := range observation.Files {
		if observation.Files[i].Path == target {
			baseline = &observation.Files[i]
			break
		}
	}
	if baseline == nil || baseline.Content != "before\n" || baseline.Origin != "" ||
		!slices.ContainsFunc(observation.Programs, func(program execProgram) bool { return program.Label == "python3" && program.Direct }) {
		t.Fatalf("same-cell direct baseline/program = %+v / %+v", baseline, observation.Programs)
	}
	writeTestFile(t, target, "after\n")
	reviews, complete, coverage, _ := reconcileExecObservation(*observation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || len(reviews) != 1 || reviews[0].Origin != "" ||
		!strings.Contains(reviews[0].Diff, "-before\n+after\n") {
		t.Fatalf("same-cell direct review = %+v complete=%v coverage=%q", reviews, complete, coverage)
	}
}

func TestExecResultState(t *testing.T) {
	exited := func(code string) json.RawMessage {
		return mustMarshalJSON("Chunk ID: 1\nWall time: 0.1 seconds\nProcess exited with code " + code + "\nOutput:\n")
	}
	if terminal, exit, completed, _, _ := execResultState(nativeExecCommandToolName, exited("0")); !terminal || !completed || exit == nil || *exit != 0 {
		t.Fatal("exit 0 was not a completed result")
	}
	if terminal, exit, completed, _, _ := execResultState(nativeExecCommandToolName, exited("2")); !terminal || completed || *exit != 2 {
		t.Fatal("exit 2 was not a failed result")
	}
	running := mustMarshalJSON("Wall time: 1.0 seconds\nProcess running with session ID 7\nOutput:\n")
	if terminal, _, _, _, pending := execResultState(nativeExecCommandToolName, running); terminal || pending != "session:7" {
		t.Fatalf("running session pending = %q", pending)
	}
	if terminal, _, completed, _, _ := execResultState(nativeExecCommandToolName, mustMarshalJSON("aborted by user")); !terminal || completed {
		t.Fatal("an unparsable result was not a terminal failure")
	}
	if terminal, _, _, _, _ := execResultState("write_stdin", mustMarshalJSON("write_stdin failed: stdin is closed for this session")); terminal {
		t.Fatal("a failed write ended a running session")
	}
	if terminal, _, completed, _, _ := execResultState("write_stdin", mustMarshalJSON("Unknown process id 7")); !terminal || completed {
		t.Fatal("a vanished session was not a terminal failure")
	}
}

func streamNativeExecCommand(t *testing.T, transform *mekugiResponseTransform, callID, arguments string) {
	t.Helper()
	item := map[string]any{"type": "function_call", "id": callID + "-item", "call_id": callID, "name": nativeExecCommandToolName, "arguments": arguments, "status": "completed"}
	added := mustMarshalJSON(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{
		"type": "function_call", "id": callID + "-item", "call_id": callID, "name": nativeExecCommandToolName, "arguments": "", "status": "in_progress",
	}})
	delta := mustMarshalJSON(map[string]any{"type": "response.function_call_arguments.delta", "item_id": callID + "-item", "delta": arguments})
	argumentsDone := mustMarshalJSON(map[string]any{"type": "response.function_call_arguments.done", "item_id": callID + "-item", "arguments": arguments})
	done := mustMarshalJSON(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	for _, event := range [][]byte{added, delta, argumentsDone, done} {
		visible, err := transform.TransformSSE(event)
		if err != nil || len(visible) != 1 || string(visible[0]) != string(event) {
			t.Fatalf("stock exec_command stream changed: visible=%q err=%v", visible, err)
		}
	}
}

func reconcileExecItems(t *testing.T, proxy *mekugiProxy, workspace string, items []any) *mekugiResponseTransform {
	t.Helper()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "tools": testNativeResponsesTools(), "tool_choice": "auto",
		"input": append(items, map[string]any{"role": "user", "content": "continue"}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	transform, err := proxy.prepareRequest(t.Context(), &request, "exec-session-next", "stock-thread", codexTurnMetadata{
		RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	return transform
}

func nativeExecOutput(state string) string {
	return "Chunk ID: 1\nWall time: 0.1 seconds\n" + state + "\nOutput:\n"
}

func TestNativeExecCommandRecordsDeclaredEffects(t *testing.T) {
	for _, test := range []struct {
		name, status string
		yielded      bool
		exit         string
		wantOutcome  string
	}{
		{name: "completed", exit: "0", wantOutcome: "command completed · exit 0"},
		{name: "failed", exit: "1", wantOutcome: "command failed · exit 1"},
		{name: "yielded", yielded: true, exit: "0", wantOutcome: "command completed · exit 0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			workspace := t.TempDir()
			target := filepath.Join(workspace, "a.txt")
			writeTestFile(t, target, "gone\n")
			transform := prepareNativeStockTransform(t, proxy, workspace, "exec-session")
			arguments := string(mustMarshalJSON(map[string]any{"cmd": "rm a.txt", "workdir": workspace, "yield_time_ms": 1000}))
			streamNativeExecCommand(t, transform, "exec-call", arguments)
			pane := newActivityPane(t.Context(), nil)
			pane.state, pane.root = activityPaneAttached, "stock-thread"
			proxy.activity.attachPane(pane)
			proxy.activity.collect("stock-thread", "tool-call\x00exec-call-item", "tool", "Run `rm a.txt`")
			if history := transform.local["exec-call"]; history.ExecObservation == nil || history.CarrierPayload != arguments {
				t.Fatalf("stock exec_command was not retained before exposure: %+v", history)
			}
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			call := map[string]any{"type": "function_call", "id": "exec-call-item", "call_id": "exec-call", "name": nativeExecCommandToolName, "arguments": arguments}
			items := []any{call}
			if test.yielded {
				items = append(items, map[string]any{"type": "function_call_output", "call_id": "exec-call", "output": nativeExecOutput("Process running with session ID 9")})
				reconcileExecItems(t, proxy, workspace, items)
				if _, found, _ := proxy.replayStore.lookup(t.Context(), workspace, "exec-call:exec:1"); found {
					t.Fatal("a running session was finalized")
				}
				items = append(items,
					map[string]any{"type": "function_call", "call_id": "stdin-call", "name": "write_stdin", "arguments": `{"session_id":9,"chars":""}`},
					map[string]any{"type": "function_call_output", "call_id": "stdin-call", "output": nativeExecOutput("Process exited with code " + test.exit)},
				)
			} else {
				items = append(items, map[string]any{"type": "function_call_output", "call_id": "exec-call", "output": nativeExecOutput("Process exited with code " + test.exit)})
			}
			next := reconcileExecItems(t, proxy, workspace, items)
			history, found, err := proxy.replayStore.lookup(t.Context(), workspace, "exec-call:exec:1")
			if err != nil || !found || history.ChangeID == "" || len(history.ReviewFiles) != 1 {
				t.Fatalf("derived record = %+v found=%v err=%v", history, found, err)
			}
			if history.ExecOutcome == nil || !strings.Contains(history.ExecOutcome.text(), test.wantOutcome) {
				t.Fatalf("host outcome = %+v, want %q", history.ExecOutcome, test.wantOutcome)
			}
			if test.name == "completed" {
				var entries []activityPaneEntry
				for _, event := range proxy.activity.events {
					if event.kind == "tool" && event.callID == "exec-call-item" {
						entries = append(entries, activityPaneEntry{Seq: uint64(len(entries) + 1), Agent: "/root", Kind: event.kind, CallID: event.callID, Text: event.raw})
					}
				}
				view := newLiveActivityView()
				view.apply(activityPaneEvent{Kind: "entries", Entries: entries})
				if len(entries) != 2 || len(view.blocks) != 1 || view.blocks[0][0].verb != "Delete" {
					t.Fatalf("observed exec receipt did not replace Run: entries=%+v blocks=%+v events=%+v", entries, view.blocks, proxy.activity.events)
				}
			}
			changes, err := proxy.replayStore.readChanges(next.ctx, changeReadOptions{
				workspace: workspace, ids: []string{history.ChangeID}, view: "history", maxTokens: 4000,
			})
			if err != nil || !strings.Contains(changes, "-gone") || !strings.Contains(changes, "exec_command input:\nrm a.txt") ||
				!strings.Contains(changes, "observed scope: a.txt") {
				t.Fatalf("mchanges history = %q, %v", changes, err)
			}
			// Replay of the same visible input neither re-reads nor re-allocates.
			reconcileExecItems(t, proxy, workspace, items)
			again, _, _ := proxy.replayStore.lookup(t.Context(), workspace, "exec-call:exec:1")
			if again.ChangeID != history.ChangeID {
				t.Fatalf("replay reallocated change %s -> %s", history.ChangeID, again.ChangeID)
			}
		})
	}
}

func TestNativeExecCommandWithoutEffectAllocatesNoChange(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	transform := prepareNativeStockTransform(t, proxy, workspace, "exec-session")
	arguments := string(mustMarshalJSON(map[string]any{"cmd": "rm -f missing.txt"}))
	streamNativeExecCommand(t, transform, "exec-call", arguments)
	reconcileExecItems(t, proxy, workspace, []any{
		map[string]any{"type": "function_call", "call_id": "exec-call", "name": nativeExecCommandToolName, "arguments": arguments},
		map[string]any{"type": "function_call_output", "call_id": "exec-call", "output": nativeExecOutput("Process exited with code 0")},
	})
	history, found, err := proxy.replayStore.lookup(t.Context(), workspace, "exec-call:exec:1")
	if err != nil || !found || history.ChangeID != "" || len(history.ReviewFiles) != 0 {
		t.Fatalf("empty observation = %+v found=%v err=%v", history, found, err)
	}
}

func TestCodeModeExecCommandRecordsUnconfirmedEffects(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
	transform.sessionShell = "bash"
	writeTestFile(t, filepath.Join(workspace, "f"), "old\n")
	call := map[string]any{
		"type": "custom_tool_call", "id": "code-item", "call_id": "code-call", "name": "exec",
		"input": `text(await tools.exec_command({cmd: "printf 'new\\n' > f"}));`, "status": "completed",
	}
	if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "response", "status": "completed", "output": []any{call}})); err != nil {
		t.Fatal(err)
	}
	if history := transform.local["code-call"]; history.ExecObservation == nil || !history.ExecObservation.CodeMode {
		t.Fatalf("Code Mode command was not observed: %+v", history)
	}
	writeTestFile(t, filepath.Join(workspace, "f"), "new\n")
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustTestJSON(t, []any{call, map[string]any{
		"type": "custom_tool_call_output", "call_id": "code-call", "output": []any{
			map[string]any{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"},
		},
	}})}}
	if _, err := proxy.reconcileVisibleInput(transform.ctx, &request, workspace, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	history, found, err := proxy.replayStore.lookup(t.Context(), workspace, "code-call:effects")
	if err != nil || !found || history.ChangeID == "" || len(history.ReviewFiles) != 1 {
		t.Fatalf("Code Mode record = %+v found=%v err=%v", history, found, err)
	}
	if history.ExecOutcome == nil || history.ExecOutcome.Status != "" {
		t.Fatalf("missing host exit must not claim a command result: %+v", history.ExecOutcome)
	}
}

func TestLiveDiffShellFileOperationPreviews(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "a.txt"), "a\n")
	writeTestFile(t, filepath.Join(workspace, "log"), "one\n")
	for _, test := range []struct {
		command string
		want    []string
	}{
		{"cp a.txt b.txt", []string{"Create b.txt (copy of a.txt)"}},
		{"mv a.txt c.txt", []string{"Move a.txt -> c.txt"}},
		{"rm -f a.txt missing", []string{"Delete a.txt"}},
		{"tee -a log <<'EOF' >/dev/null\ntwo\nEOF\n", []string{"Edit log"}},
	} {
		statements, directory, partial, parsed := liveDiffShellStatements(test.command, workspace)
		if !parsed || len(statements) != 1 {
			t.Fatalf("%q did not parse", test.command)
		}
		files, recognized, err := liveDiffShellWriteStatement(t.Context(), statements[0], directory, partial)
		if !recognized || err != nil {
			t.Fatalf("%q recognized=%v err=%v", test.command, recognized, err)
		}
		if got := reviewSummary(workspace, files); !slices.Equal(got, test.want) {
			t.Errorf("%q preview = %q, want %q", test.command, got, test.want)
		}
	}
	if _, recognized, _ := liveDiffShellWriteStatement(t.Context(), mustShellStatement(t, "cp -r a b"), workspace, false); recognized {
		t.Error("an unsupported cp option was previewed")
	}
}

func mustShellStatement(t *testing.T, command string) *syntax.Stmt {
	t.Helper()
	statements, _, _, parsed := liveDiffShellStatements(command, "/")
	if !parsed || len(statements) != 1 {
		t.Fatalf("%q did not parse", command)
	}
	return statements[0]
}

func TestExecReceiptAndSummaryText(t *testing.T) {
	workspace := t.TempDir()
	history := mekugiHistory{
		ExecOutcome: &execOutcome{Status: execStatusCompleted, Labels: []string{"cp"}},
		ReviewFiles: []mekugi.ReviewFile{
			func() mekugi.ReviewFile {
				file := mekugi.RenderReviewFile("", filepath.Join(workspace, "b.txt"), "", "a\n")
				file.CopyFrom = filepath.Join(workspace, "a.txt")
				return file
			}(),
			mekugi.RenderBinaryReviewFile(filepath.Join(workspace, "blob"), filepath.Join(workspace, "blob"), 2, 2, "aa", "bb"),
		},
	}
	want := "Create `b.txt` (copy of `a.txt`) +1 -0 · cp\n```diff\n@@ -0,0 +1,1 @@\n+a\n```\n\nEdit `blob` binary · cp"
	if got := editReceiptText(workspace, history); got != want {
		t.Fatalf("receipt = %q, want %q", got, want)
	}
}
