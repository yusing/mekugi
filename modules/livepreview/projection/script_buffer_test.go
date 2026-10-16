package projection

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveDiffPythonScriptPrefixesDoNotFlashSource(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "target.txt"), "old\n")
	w := struct{ ctx context.Context }{ctx: t.Context()}
	prefix := "cat >edit.py <<'PY'\n"
	for _, line := range []string{"from pathlib import Path\n", "p=Path('target.txt')\n"} {
		prefix += line
		files, _, _, err := Shell(w.ctx, prefix, directory, false)
		if err != nil || len(files) != 0 {
			t.Fatalf("setup prefix exposed script source: files=%+v err=%v", files, err)
		}
	}
	prefix += "s=p.read_text()\ns=s.replace('old','new')\n"
	files, _, recognized, err := Shell(w.ctx, prefix, directory, false)
	if err != nil || !recognized || len(files) != 1 || files[0].AfterPath != filepath.Join(directory, "edit.py") {
		t.Fatalf("read-only prefix was treated as a target write: files=%+v err=%v", files, err)
	}
	files, _, recognized, err = Shell(w.ctx, prefix+"p.write_text(s)\n", directory, false)
	if err != nil || !recognized || len(files) != 1 || files[0].AfterPath != filepath.Join(directory, "target.txt") {
		t.Fatalf("write prefix failed to show target: files=%+v err=%v", files, err)
	}
	files, _, recognized, err = Shell(w.ctx, "cat >ordinary.py <<'PY'\nprint('hello')\nPY\n", directory, true)
	if err != nil || !recognized || len(files) != 1 || files[0].AfterPath != filepath.Join(directory, "ordinary.py") {
		t.Fatalf("completed ordinary Python source was hidden: files=%+v err=%v", files, err)
	}
}

func TestLiveDiffPythonSourceStreamsAfterSetup(t *testing.T) {
	directory := t.TempDir()
	w := struct{ ctx context.Context }{ctx: t.Context()}
	// Setup shared with edit scripts stays hidden, including open().read().
	prefix := "cat >edit.py <<'PY'\n"
	for _, line := range []string{"\"\"\"Tool.\"\"\"\n", "import sys\n", "from pathlib import Path\n", "ROOT = Path(__file__).resolve().parent\n",
		"p = ROOT / 'target.txt'\n", "sys.path.insert(0, str(ROOT))\n", "s = open(p).read()\n"} {
		prefix += line
		if files, _, _, err := Shell(w.ctx, prefix, directory, false); err != nil || len(files) != 0 {
			t.Fatalf("setup prefix %q exposed script source: files=%+v err=%v", line, files, err)
		}
	}
	// Ordinary source streams before its heredoc closes.
	prefix = "cat >module.py <<'PY'\nimport os\n\nLIMIT = compute(3)\n"
	files, _, recognized, err := Shell(w.ctx, prefix, directory, false)
	if err != nil || !recognized || len(files) != 1 || files[0].AfterPath != filepath.Join(directory, "module.py") ||
		!strings.Contains(files[0].Diff, "+LIMIT = compute(3)") {
		t.Fatalf("ordinary Python source was held: files=%+v err=%v", files, err)
	}
}

func TestLiveDiffPythonBufferRejectsUnsupportedMutations(t *testing.T) {
	for _, mutation := range []string{"s *= 2", "s, other = 'wrong', 'value'", "del s", "(s := 'wrong')"} {
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
	ctx := WithSources(t.Context(), false)
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
