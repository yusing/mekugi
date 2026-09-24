package router

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestLiveDiffCodeModeInterpreterScriptSyntax(t *testing.T) {
	t.Parallel()
	batch := "nl -ba semantic.ts; rg CHECK runner.ts; python3 - <<'PY'\nimport pathlib\nprint(pathlib.Path('semantic.ts'))\nPY\n"
	batchInput, batchSpans := codeModeShellDisplay([]string{batch})
	batchRows := liveDiffSourceRows(batchInput, batchSpans)
	if batchRows[2].Path != "stream.py" || batchRows[3].Path != "stream.py" || batchRows[4].Path != "stream.sh" {
		t.Fatalf("inline batch interpreter syntax = %+v", batchRows)
	}
	t.Run("batch producer", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			workspace := t.TempDir()
			broker, sub, worker := newLiveDiffCodeModeWorkerTest(t, workspace)
			worker.appendDelta("text(await tools.exec_command({cmd:" + strconv.Quote(batch))
			preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
				return strings.Contains(preview.Input, "\nPY\n")
			})
			rows := liveDiffSourceRows(preview.Input, preview.Syntax)
			if rows[2].Path != "stream.py" || rows[4].Path != "stream.sh" {
				t.Fatalf("producer lost embedded interpreter span: %+v", preview.Syntax)
			}
			var pane liveDiffPreviewPane
			pane.update(preview)
			lines, err := pane.render(t.Context(), workspace, livediff.DarkTheme, 100, 15)
			if err != nil || !strings.Contains(strings.Join(lines, "\n"), livediff.DarkTheme.Foreground(chroma.KeywordNamespace)+"import") {
				t.Fatalf("producer left embedded Python plain: %v %q", err, lines)
			}
		})
	})
	for _, tc := range []struct {
		name, command, language, token string
		kind                           chroma.TokenType
	}{
		{"python heredoc", "python3 - <<'PY'\nfor n in range(1):\n    print(n)\nPY\n", "stream.py", "for", chroma.Keyword},
		{"python command", "python3 -c 'for n in range(1):\n    print(n)'", "stream.py", "for", chroma.Keyword},
		{"node command", "node -e 'const value = 1;\nconsole.log(value)'", "stream.ts", "const", chroma.KeywordDeclaration},
		{"bun heredoc", "bun - <<'JS'\nconst value = 1;\nconsole.log(value)\nJS\n", "stream.ts", "const", chroma.KeywordDeclaration},
		{"bun command", "bun -e 'const value = 1; console.log(value)'", "stream.ts", "const", chroma.KeywordDeclaration},
		{"perl heredoc", "perl - <<'PL'\nmy $value = 1;\nprint $value;\nPL\n", "stream.pl", "my", chroma.KeywordDeclaration},
		{"perl command", "perl -e 'my $value = 1; print $value;'", "stream.pl", "my", chroma.KeywordDeclaration},
		{"ruby command", "ruby -e 'if true\n  puts \"ok\"\nend'", "stream.rb", "if", chroma.Keyword},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				workspace := t.TempDir()
				broker, sub, worker := newLiveDiffCodeModeWorkerTest(t, workspace)
				worker.appendDelta("text(await tools.exec_command({cmd:" + strconv.Quote(tc.command))
				preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
					return strings.Contains(preview.Input, tc.token)
				})
				if len(preview.Syntax) != 2 || preview.Syntax[0].Path != "stream.sh" || preview.Syntax[1].Path != tc.language {
					t.Fatalf("interpreter preview syntax = %+v, input = %q", preview.Syntax, preview.Input)
				}
				var pane liveDiffPreviewPane
				pane.update(preview)
				lines, err := pane.render(t.Context(), workspace, livediff.DarkTheme, 100, 15)
				if err != nil || !strings.Contains(strings.Join(lines, "\n"), livediff.DarkTheme.Foreground(tc.kind)+tc.token) {
					t.Fatalf("missing %s color: %v %q", tc.token, err, lines)
				}
			})
		})
	}

	commands := []string{"printf before", "python3 - <<'PY'\nfor n in range(1):\n    print(n)\nPY\n", "printf after"}
	input, spans := codeModeShellDisplay(commands)
	rows := liveDiffSourceRows(input, spans)
	pythonRow := strings.Count(input[:strings.Index(input, "for n")], "\n")
	if len(spans) != 4 || rows[1].Path != "stream.sh" || rows[pythonRow].Path != "stream.py" || rows[len(rows)-1].Path != "stream.sh" {
		t.Fatalf("mixed shell and interpreter syntax = %+v; input = %q", spans, input)
	}
}

func TestLiveDiffTerminalCodeModePythonHeredocSyntax(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 18, "COLORFGBG=15;0")
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", "exec")
	t.Cleanup(worker.stop)
	command := "python3 - <<'PY'\nfor n in range(1):\n    print(n)\nPY\n"
	input := "text(await tools.exec_command({cmd:" + strconv.Quote(command) + "}));"
	split := strings.Index(input, "    print(n)")
	if split < 0 {
		t.Fatal("fixture does not contain a Python body")
	}
	worker.appendDelta(input[:split])
	colored := livediff.DarkTheme.Foreground(chroma.Keyword) + "for"
	frame := ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "STREAMING SCRIPT") && strings.Contains(frame, colored)
	})
	if strings.Contains(ansi.Strip(frame), "python3 - <<") {
		t.Fatalf("shell framing displaced Python source: %q", frame)
	}
	worker.appendDelta(input[split:])
	ui.frame(t, func(frame string) bool {
		return strings.Contains(ansi.Strip(frame), "print(n)") && strings.Contains(frame, colored)
	})
	worker.finish(input)
	ui.frame(t, func(frame string) bool {
		return strings.Contains(frame, "STREAMING COMPLETE") && strings.Contains(frame, colored)
	})
	ui.quit(t)
}

func TestLiveDiffCodeModeSSEStreamsBashWithoutChangingEvents(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	proxy := newManagedMekugiProxy(t)
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription)},
		"tools": []any{},
	}))
	if err != nil {
		t.Fatal(err)
	}
	transform, err := proxy.prepareRequest(t.Context(), &request, "stream-session", "thread", codexTurnMetadata{
		RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := broker.subscribe()
	<-sub.events
	proxy.autoLiveDiff = &autoLiveDiff{events: broker, requested: true,
		scope: liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}}}
	proxy.autoLiveDiff.enabled.Store(true)
	for _, event := range [][]byte{
		mustTestJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"type": "custom_tool_call", "id": "exec-item", "call_id": "exec-call", "name": "exec", "input": "", "status": "in_progress"}}),
		mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "exec-item",
			"delta": `const r=await Promise.allSettled([tools.exec_command({cmd:"printf first\nprintf again"})`}),
	} {
		visible, err := transform.TransformSSE(event)
		if err != nil || len(visible) != 1 || !bytes.Equal(visible[0], event) {
			t.Fatalf("stock SSE event changed: %q, %v", visible, err)
		}
	}
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return strings.Contains(preview.Input, "printf again")
	})
	assertCodeModeBashPreview(t, preview, []string{"# tools.exec_command 1", "printf first\nprintf again"}, "Promise.allSettled")
	second := mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": "exec-item",
		"delta": `,tools.exec_command({cmd:"printf second"})]);for(let i=0;i<r.length;i++)text(JSON.stringify({i,...r[i]}))`})
	visible, err := transform.TransformSSE(second)
	if err != nil || len(visible) != 1 || !bytes.Equal(visible[0], second) {
		t.Fatalf("stock second SSE event changed: %q, %v", visible, err)
	}
	preview = waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return strings.Contains(preview.Input, "printf second")
	})
	assertCodeModeBashPreview(t, preview, []string{"# tools.exec_command 1", "printf first\nprintf again", "# tools.exec_command 2", "printf second"}, "JSON.stringify")
}

func TestLiveDiffCodeModeStreamsBatchedCommandsAsBash(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker, sub, worker := newLiveDiffCodeModeWorkerTest(t, workspace)

	firstChunk := `const results = await Promise.allSettled([
tools.exec_command({cmd:"printf 'first'\nprintf 'first-tail'", note:"tools.exec_command({cmd:'printf fake-string'})"})`
	fragments, detected := codeModeShellFragments(firstChunk)
	if !detected || len(fragments) != 1 || fragments[0] != "printf 'first'\nprintf 'first-tail'" {
		t.Fatalf("partial batch fixture was not scanned as one literal Bash command: %t %#v", detected, fragments)
	}
	worker.appendDelta(firstChunk)
	first := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return strings.Contains(preview.Input, "printf 'first-tail'")
	})
	assertCodeModeBashPreview(t, first, []string{
		"# tools.exec_command 1",
		"printf 'first'\nprintf 'first-tail'",
	}, firstChunk)
	if first.Complete {
		t.Fatal("preview claimed completion before the JavaScript batch was complete")
	}

	secondChunk := `,
tools.exec_command({cmd:"printf 'second'"})
]);
for (let i=0;i<results.length;i++) text(JSON.stringify({i,...results[i]}));
// tools.exec_command({cmd:"printf fake-comment"})`
	worker.appendDelta(secondChunk)
	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return strings.Contains(preview.Input, "printf 'second'")
	})
	assertCodeModeBashPreview(t, complete, []string{
		"# tools.exec_command 1",
		"printf 'first'\nprintf 'first-tail'",
		"# tools.exec_command 2",
		"printf 'second'",
	}, "const results", "Promise.allSettled", "for (let i=", "JSON.stringify", "fake-string", "fake-comment")

	var pane liveDiffPreviewPane
	pane.update(complete)
	lines, err := pane.render(t.Context(), workspace, livediff.DarkTheme, 100, 12)
	if err != nil {
		t.Fatal(err)
	}
	rendered := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"STREAMING SCRIPT", "# tools.exec_command 1", "# tools.exec_command 2", "printf 'first-tail'", "printf 'second'"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered Bash preview missing %q: %q", want, rendered)
		}
	}
	for _, leaked := range []string{"Promise.allSettled", "JSON.stringify", "fake-string", "fake-comment"} {
		if strings.Contains(rendered, leaked) {
			t.Errorf("rendered preview leaked JavaScript wrapper or decoy %q: %q", leaked, rendered)
		}
	}
}

func TestLiveDiffTerminalStreamsCodeModeBashBoundaries(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 18)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", "exec")
	t.Cleanup(worker.stop)
	worker.appendDelta(`const r=await Promise.allSettled([tools.exec_command({cmd:"printf first\nprintf again"})`)
	first := ui.frame(t, func(frame string) bool {
		plain := ansi.Strip(frame)
		return strings.Contains(plain, "# tools.exec_command 1") && strings.Contains(plain, "printf again")
	})
	if strings.Contains(first, "Promise.allSettled") {
		t.Fatal("terminal displayed JavaScript instead of the first shell command")
	}
	worker.appendDelta(`,tools.exec_command({cmd:"printf second"})]);for(let i=0;i<r.length;i++)text(JSON.stringify({i,...r[i]}))`)
	second := ui.frame(t, func(frame string) bool {
		plain := ansi.Strip(frame)
		return strings.Contains(plain, "# tools.exec_command 1") && strings.Contains(plain, "# tools.exec_command 2") &&
			strings.Contains(plain, "printf second")
	})
	if strings.Contains(second, "JSON.stringify") || strings.Contains(second, "Promise.allSettled") {
		t.Fatal("terminal displayed JavaScript after the batch completed")
	}
	ui.quit(t)
}

func TestLiveDiffCodeModeRejectsDynamicShellPreviewAndKeepsNonShellJS(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker, sub, worker := newLiveDiffCodeModeWorkerTest(t, workspace)
	worker.appendDelta(`tools.exec_command({cmd:"printf safe"`)
	visible := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return strings.Contains(preview.Input, "printf safe")
	})
	worker.appendDelta(` + suffix})`)
	removed := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.ID == visible.ID && preview.Workspace == ""
	})
	if removed.Input != "" || removed.Status != "" {
		t.Fatalf("dynamic command kept stale literal preview: %+v", removed)
	}
	worker.stop()

	otherBroker, sub, other := newLiveDiffCodeModeWorkerTest(t, workspace)
	const source = `const r=await Promise.allSettled(tasks);text(r)`
	other.appendDelta(source)
	preview := waitLiveDiffWorkerPreview(t, otherBroker, sub, func(preview liveDiffPreview) bool {
		return preview.Input == source
	})
	if preview.Status != "STREAMING SCRIPT" || len(preview.Syntax) == 0 || preview.Syntax[0].Path != "preview.js" {
		t.Fatalf("non-shell Code Mode stream lost JavaScript view: %+v", preview)
	}
}

func TestLiveDiffCodeModeRetractsJSWhenBatchPrefixArrives(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker, sub, worker := newLiveDiffCodeModeWorkerTest(t, workspace)
	const prefix = `const r=await Promise.`
	worker.appendDelta(prefix)
	visible := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Input == prefix
	})
	worker.appendDelta(`allSettled([`)
	removed := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.ID == visible.ID && preview.Workspace == ""
	})
	if removed.Input != "" || removed.Status != "" {
		t.Fatalf("batch prefix left JavaScript visible: %+v", removed)
	}
	worker.appendDelta(`tools.exec_command({cmd:"printf ready"})`)
	bash := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return strings.Contains(preview.Input, "printf ready")
	})
	assertCodeModeBashPreview(t, bash, []string{"# tools.exec_command 1", "printf ready"}, "Promise.allSettled")
}

func TestLiveDiffPartialShellDoesNotProjectEditsInWrongWorkdir(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker, sub, worker := newLiveDiffCodeModeWorkerTest(t, workspace)
	source := `tools.exec_command({cmd:"cat > result.txt <<'EOF'\ncontent\nEOF",workdir:"/other/workspace"`
	if scripts, candidate := codeModeShellFragments(source); !candidate || len(scripts) != 1 {
		t.Fatalf("partial command not recognized: candidate %t, scripts %#v", candidate, scripts)
	}
	worker.appendDelta(source)
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return strings.Contains(preview.Input, "# tools.exec_command 1")
	})
	if preview.Workspace != workspace || len(preview.Files) != 0 || preview.Status != "STREAMING SCRIPT" {
		t.Fatalf("partial command projected a filesystem edit before workdir was validated: %+v", preview)
	}
}

func newLiveDiffCodeModeWorkerTest(t *testing.T, workspace string) (*liveDiffBroker, *liveDiffSubscriber, *liveDiffPreviewWorker) {
	t.Helper()
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := broker.subscribe()
	<-sub.events
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", "exec")
	t.Cleanup(worker.stop)
	return broker, sub, worker
}

func assertCodeModeBashPreview(t *testing.T, preview liveDiffPreview, ordered []string, absent ...string) {
	t.Helper()
	if preview.Status != "STREAMING SCRIPT" {
		t.Fatalf("preview status = %q, want STREAMING SCRIPT; input %q", preview.Status, preview.Input)
	}
	if len(preview.Syntax) == 0 || preview.Syntax[0].Path != "stream.sh" {
		t.Fatalf("preview syntax = %+v, want stream.sh", preview.Syntax)
	}
	position := 0
	for _, want := range ordered {
		relative := strings.Index(preview.Input[position:], want)
		if relative < 0 {
			t.Fatalf("Bash preview %q missing ordered text %q after byte %d", preview.Input, want, position)
		}
		position += relative + len(want)
	}
	for _, unexpected := range absent {
		if strings.Contains(preview.Input, unexpected) {
			t.Errorf("Bash preview leaked %q: %q", unexpected, preview.Input)
		}
	}
}

func TestLiveDiffCodeModeRevealsWholeShellUnits(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker, sub, worker := newLiveDiffCodeModeWorkerTest(t, workspace)
	source := `tools.exec_command({cmd:"cat f | head; echo \"a | b\" && python3 -c 'import os\nprint(1); f()\n'"})`
	go func() {
		for at := 0; at < len(source); at += 4 {
			worker.appendDelta(source[at:min(at+4, len(source))])
			time.Sleep(3 * time.Millisecond)
		}
		worker.finish(source)
	}()
	seen := 0
	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		if preview.Complete {
			return true
		}
		if preview.Input != "" {
			seen++
			// Streaming frames end on a list operator, line, or statement end.
			if end := liveDiffScriptBoundary(preview.Input, preview.Syntax); end != len(preview.Input) ||
				strings.HasSuffix(preview.Input, "|") || strings.HasSuffix(preview.Input, `"a |`) {
				t.Errorf("streaming frame ended mid-unit: %q", preview.Input)
			}
		}
		return false
	})
	if seen == 0 || !strings.HasSuffix(complete.Input, "f()\n'") {
		t.Fatalf("stream frames %d, final %q", seen, complete.Input)
	}
}
