package router

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
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
	worker.Append(input[:split])
	first := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+first")
	})
	if first.Complete || first.Input != "" || first.Status != previewmodel.PreviewEdit {
		t.Fatalf("partial exec cat was not a provisional edit: %+v", first)
	}
	if !strings.Contains(first.Files[0].Diff, "-old") {
		t.Fatalf("partial cat did not predict truncation: %q", first.Files[0].Diff)
	}
	worker.Append(input[split:])
	worker.Finish(input)
	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool { return preview.Complete })
	if len(complete.Files) != 1 || !strings.Contains(complete.Files[0].Diff, "+second") {
		t.Fatalf("completed exec cat lost its diff: %+v", complete)
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
			worker.Append(input[:strings.Index(input, tc.split)])
			preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
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

func TestLiveDiffCompletionOnlyShellDoesNotOpenNativeDock(t *testing.T) {
	for _, kind := range []string{nativeExecCommandToolName, "exec"} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			broker := newLivePreview(t.Context())
			setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
			sub := testReview(broker).Subscribe()
			<-sub.Events()
			worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", kind)
			t.Cleanup(worker.Stop)
			command := "python3 - <<'PY'\nfrom pathlib import Path\nPath('late.txt').write_text('first\\nsecond\\nthird\\n')\nPY\n"
			input := string(mustTestJSON(t, map[string]string{"cmd": command}))
			if kind == "exec" {
				input = "text(await tools.exec_command(" + input + "));"
			}
			worker.Finish(input)
			u := &terminalUI{}
			preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
				u.preview(preview, false)
				if len(u.liveDock.Order) != 0 {
					t.Fatal("completion-only worker manufactured a live dock")
				}
				return preview.Complete
			})
			if len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "+third") {
				t.Fatalf("completion lost final projection: %+v", preview)
			}
		})
	}
}

func TestLiveDiffCodeModeSSEStreamsEditWithoutChangingEvents(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	proxy := newManagedMekugiProxy(t)
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription)},
		"tools": []any{},
	}))
	if err != nil {
		t.Fatal(err)
	}
	broker := newLivePreview(t.Context())
	proxy.review = testReview(broker)
	transform, err := proxy.prepareRequest(t.Context(), &request, "stream-session", "thread", codexresponses.TurnMetadata{
		RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	<-sub.Events()

	proxy.review.Preview().Enable()
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
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+streamed")
	})
	if preview.Input != "" || preview.Complete {
		t.Fatalf("exec stream exposed script text or claimed completion: %+v", preview)
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
			worker.Append(input)
			worker.Finish(input)
			waitLiveDiffWorkerDone(t, worker)
			for _, event := range testReview(broker).TakePreviews(sub) {
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
	worker.Append(`const r=await Promise.allSettled([tools.exec_command({cmd:"cat > kept.txt <<'EOF'\nkept\nEOF\n"}),`)
	waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+kept")
	})
	worker.Append(`tools.exec_command({cmd:"go test ./..."})]);text(r)`)
	worker.Finish(`const r=await Promise.allSettled([tools.exec_command({cmd:"cat > kept.txt <<'EOF'\nkept\nEOF\n"}),tools.exec_command({cmd:"go test ./..."})]);text(r)`)
	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool { return preview.Complete })
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
	worker.Append(`tools.exec_command({cmd:"cat > result.txt <<'EOF'\ncontent\nEOF",workdir:` + strconv.Quote(other))
	// Earlier paced frames may predate the workdir; the streamed workdir wins.
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return len(preview.Files) == 1 && preview.Files[0].AfterPath == filepath.Join(other, "result.txt")
	})
	if preview.Workspace != workspace || !strings.Contains(preview.Files[0].Diff, "-other") {
		t.Fatalf("partial command projected outside its workdir: %+v", preview)
	}

	broker, sub, worker = newLiveDiffCodeModeWorkerTest(t, workspace)
	input := `tools.exec_command({cmd:"cat > result.txt <<'EOF'\ncontent\nEOF",workdir:dir})`
	worker.Append(input)
	worker.Finish(input)
	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool { return preview.Complete })
	if len(complete.Files) != 0 || complete.Status != "" || complete.Input != "" {
		t.Fatalf("computed workdir projected an edit: %+v", complete)
	}
}

func newLiveDiffCodeModeWorkerTest(t *testing.T, workspace string) (*previewmodel.Session, *sessionreview.Subscription, *previewbinding.Stream) {
	t.Helper()
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	<-sub.Events()
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", "exec")
	t.Cleanup(worker.Stop)
	return broker, sub, worker
}
