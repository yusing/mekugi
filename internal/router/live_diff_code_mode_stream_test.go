package router

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestLiveDiffCodeModeStreamsCatEditBeforeCompletion(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "out.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	broker, sub, worker := newLiveDiffCodeModeWorkerTest(t, workspace)
	command := "cat > out.txt <<'EOF'\nfirst\nsecond\nEOF\n"
	input := "const r = await tools.exec_command({cmd:" + strconv.Quote(command) + "}); text(r.output);"
	split := strings.Index(input, "second")
	worker.appendDelta(input[:split])
	first := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+first")
	})
	if first.Complete || first.Input != "" || first.Status != liveDiffPreviewEdit {
		t.Fatalf("partial Code Mode cat was not a provisional edit: %+v", first)
	}
	if !strings.Contains(first.Files[0].Diff, "-old") {
		t.Fatalf("partial cat did not predict truncation: %q", first.Files[0].Diff)
	}
	worker.appendDelta(input[split:])
	worker.finish(input)
	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool { return preview.Complete })
	if len(complete.Files) != 1 || !strings.Contains(complete.Files[0].Diff, "+second") {
		t.Fatalf("completed Code Mode cat lost its diff: %+v", complete)
	}
}

func TestLiveDiffCodeModeStreamsInterpreterWrite(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, command, split string }{
		{"python", "python3 - <<'PY'\nfrom pathlib import Path\nPath(\"out.py\").write_text(\"\"\"first\nsecond\n\"\"\")\nPY\n", "second"},
		{"node", "node - <<'JS'\nconst fs = require(\"node:fs\");\nfs.writeFileSync(\"out.js\", `first\nsecond\n`);\nJS\n", "second"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			broker, sub, worker := newLiveDiffCodeModeWorkerTest(t, workspace)
			input := "text(await tools.exec_command({cmd:" + strconv.Quote(tc.command) + "}));"
			worker.appendDelta(input[:strings.Index(input, tc.split)])
			preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
				return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+first")
			})
			if preview.Complete || preview.Files[0].BeforePath != "" {
				t.Fatalf("partial interpreter write = %+v", preview)
			}
			if entries, err := os.ReadDir(workspace); err != nil || len(entries) != 0 {
				t.Fatalf("interpreter prediction had effects: %v, %v", entries, err)
			}
		})
	}
}

func TestLiveDiffCodeModeSSEStreamsEditWithoutChangingEvents(t *testing.T) {
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
			"delta": `const r=await Promise.allSettled([tools.exec_command({cmd:"printf first"}),tools.exec_command({cmd:"cat > out.txt <<'EOF'\nstreamed\n`}),
	} {
		visible, err := transform.TransformSSE(event)
		if err != nil || len(visible) != 1 || !bytes.Equal(visible[0], event) {
			t.Fatalf("stock SSE event changed: %q, %v", visible, err)
		}
	}
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+streamed")
	})
	if preview.Input != "" || preview.Complete {
		t.Fatalf("Code Mode stream exposed script text or claimed completion: %+v", preview)
	}
}

func TestLiveDiffCodeModeNonEditHasNoPreview(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		`const r=await Promise.allSettled([tools.exec_command({cmd:"printf first\nprintf again"}),tools.exec_command({cmd:"rg TODO"})]);text(r)`,
		`const r=await Promise.allSettled(tasks);text(r)`,
		`tools.exec_command({cmd:"python3 - <<'PY'\nprint(1)\nPY\n"})`,
	} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			broker, sub, worker := newLiveDiffCodeModeWorkerTest(t, t.TempDir())
			worker.appendDelta(input)
			worker.finish(input)
			waitLiveDiffWorkerDone(t, worker)
			for _, event := range broker.takePreviews(sub) {
				if event.Preview != nil && (event.Preview.Input != "" || len(event.Preview.Files) != 0 || event.Preview.Status != "") {
					t.Fatalf("non-edit command produced a preview: %+v", *event.Preview)
				}
			}
		})
	}
}

func TestLiveDiffCodeModeKeepsEarlierEditWhileLaterCommandStreams(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker, sub, worker := newLiveDiffCodeModeWorkerTest(t, workspace)
	worker.appendDelta(`const r=await Promise.allSettled([tools.exec_command({cmd:"cat > kept.txt <<'EOF'\nkept\nEOF\n"}),`)
	waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+kept")
	})
	worker.appendDelta(`tools.exec_command({cmd:"go test ./..."})]);text(r)`)
	worker.finish(`const r=await Promise.allSettled([tools.exec_command({cmd:"cat > kept.txt <<'EOF'\nkept\nEOF\n"}),tools.exec_command({cmd:"go test ./..."})]);text(r)`)
	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool { return preview.Complete })
	if len(complete.Files) != 1 || !strings.Contains(complete.Files[0].Diff, "+kept") {
		t.Fatalf("later non-edit command replaced the displayed edit: %+v", complete)
	}
}

func TestLiveDiffPartialShellUsesStreamedWorkdir(t *testing.T) {
	t.Parallel()
	workspace, other := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "result.txt"), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	broker, sub, worker := newLiveDiffCodeModeWorkerTest(t, workspace)
	worker.appendDelta(`tools.exec_command({cmd:"cat > result.txt <<'EOF'\ncontent\nEOF",workdir:` + strconv.Quote(other))
	// Earlier paced frames may predate the workdir; the streamed workdir wins.
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return len(preview.Files) == 1 && preview.Files[0].AfterPath == filepath.Join(other, "result.txt")
	})
	if preview.Workspace != workspace || !strings.Contains(preview.Files[0].Diff, "-other") {
		t.Fatalf("partial command projected outside its workdir: %+v", preview)
	}

	broker, sub, worker = newLiveDiffCodeModeWorkerTest(t, workspace)
	input := `tools.exec_command({cmd:"cat > result.txt <<'EOF'\ncontent\nEOF",workdir:dir})`
	worker.appendDelta(input)
	worker.finish(input)
	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool { return preview.Complete })
	if len(complete.Files) != 0 || !strings.HasPrefix(complete.Status, liveDiffPreviewUnavailable) {
		t.Fatalf("computed workdir projected an edit: %+v", complete)
	}
}

func TestLiveDiffTerminalCodeModeCatStreamsDiff(t *testing.T) {
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
	command := "cat > notes.md <<'EOF'\nalpha\nbeta\nEOF\n"
	input := "text(await tools.exec_command({cmd:" + strconv.Quote(command) + "}));"
	split := strings.Index(input, "beta")
	worker.appendDelta(input[:split])
	frame := ansi.Strip(ui.frame(t, func(frame string) bool {
		plain := ansi.Strip(frame)
		return strings.Contains(plain, "◐ A notes.md +1 -0") && strings.Contains(plain, "+alpha")
	}))
	if strings.Contains(frame, "tools.exec_command") || strings.Contains(frame, "cat >") || strings.Contains(frame, "STREAMING") {
		t.Fatalf("terminal showed script text or streaming wording: %q", frame)
	}
	worker.appendDelta(input[split:])
	worker.finish(input)
	ui.frame(t, func(frame string) bool {
		return strings.Contains(ansi.Strip(frame), "✓ A notes.md +2 -0")
	})
	ui.quit(t)
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
