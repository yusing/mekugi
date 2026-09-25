package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestLiveDiffPythonScriptPrefixesDoNotFlashSource(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "target.txt"), "old\n")
	w := liveDiffPreviewWorker{ctx: t.Context()}
	prefix := "cat >edit.py <<'PY'\n"
	for _, line := range []string{"from pathlib import Path\n", "p=Path('target.txt')\n", "s=p.read_text()\n"} {
		prefix += line
		files, _, err := w.projectShell(prefix, directory, false)
		if err != nil || len(files) != 0 {
			t.Fatalf("setup prefix exposed script source: files=%+v err=%v", files, err)
		}
	}
	prefix += "s=s.replace('old','new')\n"
	files, recognized, err := w.projectShell(prefix, directory, false)
	if err != nil || !recognized || len(files) != 1 || files[0].AfterPath != filepath.Join(directory, "target.txt") {
		t.Fatalf("replacement prefix failed to show target: files=%+v err=%v", files, err)
	}
	files, recognized, err = w.projectShell("cat >ordinary.py <<'PY'\nprint('hello')\nPY\n", directory, true)
	if err != nil || !recognized || len(files) != 1 || files[0].AfterPath != filepath.Join(directory, "ordinary.py") {
		t.Fatalf("completed ordinary Python source was hidden: files=%+v err=%v", files, err)
	}
}

func TestLiveDiffPythonBufferRejectsUnsupportedMutations(t *testing.T) {
	for _, mutation := range []string{"s += '!'", "s, other = 'wrong', 'value'", "del s", "(s := 'wrong')"} {
		t.Run(mutation, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "target.txt"), "old\n")
			command := "python3 - <<'PY'\nfrom pathlib import Path\np=Path('target.txt')\ns=p.read_text()\ns=s.replace('old','new')\n" + mutation + "\np.write_text(s)\nPY\n"
			files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, command), directory, false)
			if err != nil || recognized || len(files) != 0 {
				t.Fatalf("unsupported mutation produced stale prediction: %+v, %v", files, err)
			}
		})
	}
}

func TestLiveDiffPythonBufferKeepsOriginalBaseline(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.txt")
	writeTestFile(t, target, "a\n")
	ctx := withLiveDiffSources(t.Context())
	command := "python3 - <<'PY'\nfrom pathlib import Path\np=Path('target.txt')\ns=p.read_text()\ns=s.replace('a','aa')\np.write_text(s)\nPY\n"
	statement := mustShellStatement(t, command)
	first, _, err := liveDiffInterpreterWrite(ctx, statement, directory, false)
	if err != nil || len(first) != 1 {
		t.Fatalf("first prediction: %+v, %v", first, err)
	}
	writeTestFile(t, target, "aa\n")
	final, _, err := liveDiffInterpreterWrite(ctx, statement, directory, false)
	if err != nil || len(final) != 1 || final[0].Diff != first[0].Diff {
		t.Fatalf("host execution changed predicted baseline: %+v, %v", final, err)
	}
}

func TestLiveDiffInterpreterWriteReadReplaceBuffer(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	target := filepath.Join(directory, "target.txt")
	const original = "old old marker\n"
	if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	command := "python3 - <<'PY'\n" +
		"from pathlib import Path\n" +
		"p = Path('target.txt')\n" +
		"s = p.read_text()\n" +
		"s = s.replace('old', 'middle', 1).replace('middle', 'new').replace('marker', 'done', 1)\n" +
		"p.write_text(s)\n" +
		"PY\n"

	files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, command), directory, false)
	if err != nil || !recognized || len(files) != 1 {
		t.Fatalf("buffer prediction = %+v, %t, %v; want one target diff", files, recognized, err)
	}
	if files[0].BeforePath != target || files[0].AfterPath != target ||
		!strings.Contains(files[0].Diff, "-old old marker") || !strings.Contains(files[0].Diff, "+new old done") {
		t.Fatalf("buffer diff does not reflect sequential replacements and count: %+v", files[0])
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != original {
		t.Fatalf("buffer preview changed target: got %q, %v", got, err)
	}
}

func TestLiveDiffInterpreterWriteRejectsUnsupportedEffectBeforeBufferWrite(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	target := filepath.Join(directory, "target.txt")
	const original = "old text\n"
	if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	command := "python3 - <<'PY'\n" +
		"from pathlib import Path\n" +
		"p = Path('target.txt')\n" +
		"s = p.read_text()\n" +
		"s = s.replace('old', 'new')\n" +
		"unmodeled_side_effect()\n" +
		"p.write_text(s)\n" +
		"PY\n"

	files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, command), directory, false)
	if err != nil || recognized || len(files) != 0 {
		t.Fatalf("unsupported earlier effect must reject the prediction: files=%+v recognized=%v err=%v", files, recognized, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != original {
		t.Fatalf("rejected prediction changed target: got %q, %v", got, err)
	}
}

func TestLiveDiffTerminalCatPythonScriptStreamsTargetDiff(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	target := filepath.Join(workspace, "target.txt")
	const original = "old old\n"
	if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread")
	t.Cleanup(worker.stop)

	streamed := "cat >edit.py <<'PY'\n" +
		"from pathlib import Path\n" +
		"p = Path('target.txt')\n" +
		"s = p.read_text()\n" +
		"s = s.replace('old', 'new', 1)\n" +
		"p.write_text(s)\n"
	worker.appendDelta(streamed)
	preview := waitExecScopePreview(t, broker, func(preview liveDiffPreview) bool {
		return preview.Status == liveDiffPreviewEdit && len(preview.Files) == 1 &&
			strings.Contains(preview.Files[0].Diff, "+new old")
	})
	if preview.Input != "" || !strings.Contains(preview.Files[0].Diff, "-old old") {
		t.Fatalf("streamed Python body was not projected as a target diff: %+v", preview)
	}
	worker.appendDelta("s = s.replace('new',")
	retained := waitExecScopePreview(t, broker, func(preview liveDiffPreview) bool {
		return !preview.Complete && preview.Status == liveDiffPreviewEdit && len(preview.Files) == 1 &&
			strings.Contains(preview.Files[0].Diff, "+new old")
	})
	if retained.Input != "" || retained.Files[0].AfterPath != target {
		t.Fatalf("unfinished replacement did not retain only the target diff: %+v", retained)
	}
	frame := ansi.Strip(ui.frame(t, func(frame string) bool {
		plain := ansi.Strip(frame)
		return strings.Contains(plain, "◐ M") && strings.Contains(plain, "target.txt") && strings.Contains(plain, "+new old")
	}))
	if strings.Contains(frame, "cat >edit.py") || strings.Contains(frame, "from pathlib import Path") ||
		strings.Contains(frame, "p.write_text") || strings.Contains(frame, "STREAMING") {
		t.Fatalf("terminal showed the edit script instead of its target diff: %q", frame)
	}

	if got, err := os.ReadFile(target); err != nil || string(got) != original {
		t.Fatalf("stream preview changed target: got %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "edit.py")); !os.IsNotExist(err) {
		t.Fatalf("stream preview created its script: %v", err)
	}
	worker.finish(streamed + "s = s.replace('new',\nPY\n")
	finalFrame := ansi.Strip(ui.frame(t, func(frame string) bool {
		plain := ansi.Strip(frame)
		return strings.Contains(plain, "✓ M") && strings.Contains(plain, "target.txt") && strings.Contains(plain, "+new old")
	}))
	if strings.Contains(finalFrame, "cat >edit.py") || strings.Contains(finalFrame, "from pathlib import Path") ||
		strings.Contains(finalFrame, "s = s.replace") {
		t.Fatalf("terminal showed malformed script text instead of the retained diff: %q", finalFrame)
	}
	ui.quit(t)
}
