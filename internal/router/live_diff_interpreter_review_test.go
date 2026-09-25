package router

import (
	"path/filepath"
	"testing"
)

func TestLiteralPreviewRejectsEarlierUnpredictedEffects(t *testing.T) {
	for _, earlier := range []string{`p.write_text('old'.upper())`, `p.unlink()`, `open('a', 'w')`, `unknown()`, "if True:\n    p.write_text('OLD')"} {
		t.Run(earlier, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "a"), "old")
			command := "python3 - <<'PY'\nfrom pathlib import Path\np=Path('a')\n" + earlier + "\np.write_text(p.read_text().replace('old','new'))\nPY\n"
			files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, command), directory, false)
			if recognized || len(files) != 0 || err != nil {
				t.Fatalf("dependent prediction: %+v, %v", files, err)
			}
		})
	}
}

func TestLiteralPreviewDoesNotRetargetWriteAfterPathReassignment(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "a"), "before a\n")
	writeTestFile(t, filepath.Join(directory, "b"), "before b\n")
	command := "python3 - <<'PY'\nfrom pathlib import Path\np = Path('a'); p.write_text('after a\\n'); p = Path('b')\nPY\n"
	files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, command), directory, false)
	if err != nil || recognized || len(files) != 0 {
		t.Fatalf("write before trailing Path reassignment was retargeted: files=%+v recognized=%v err=%v", files, recognized, err)
	}
}
