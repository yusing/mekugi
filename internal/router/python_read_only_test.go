package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/ui/diffview"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestPythonReadOnlyDoesNotPreviewInputEdits(t *testing.T) {
	for _, script := range []string{
		"s = open('target.txt').read()\ns = s.replace('old', 'new')\nprint(s)\n",
		"s = open('target.txt', 'r').read()\ns = s.replace('old', 'new')\nprint(s)\n",
		"s = open('target.txt', encoding='utf-8').read()\ns = s.replace('old', 'new')\nprint(s)\n",
		"with open('target.txt', mode='r', encoding='utf-8') as f:\n    s = f.read()\ns = s.replace('old', 'new')\nprint(s)\n",
		"from pathlib import Path\ns = Path('target.txt').read_text()\nprint(s)\n",
		"from pathlib import Path\ns = Path('target.txt').read_text().replace('old', 'new')\nprint(s)\n",
		"from pathlib import Path\np = Path('target.txt')\nwith p.open(encoding='utf-8') as f:\n    s = f.read()\ns = s.replace('old', 'new')\nprint(s)\n",
	} {
		t.Run(script, func(t *testing.T) {
			directory := t.TempDir()
			target := filepath.Join(directory, "target.txt")
			writeTestFile(t, target, "old\n")
			worker := liveDiffPreviewWorker{ctx: t.Context()}
			for _, final := range []bool{false, true} {
				command := "python3 - <<'PY'\n" + script + "PY\n"
				files, recognized, err := worker.projectShell(command, directory, final)
				if err != nil || recognized || len(files) != 0 {
					t.Fatalf("read-only execution invented an edit (final=%t): %+v %t %v", final, files, recognized, err)
				}
				if display := toolActivityShell(command); !strings.HasPrefix(display, "Run") {
					t.Fatalf("read-only execution displayed as edit: %s", display)
				}
				files, recognized, err = worker.projectShell("cat >report.py <<'PY'\n"+script+"PY\n", directory, final)
				if err != nil || !recognized || len(files) != 1 || files[0].AfterPath != filepath.Join(directory, "report.py") ||
					!strings.Contains(files[0].Diff, "+print(s)") {
					t.Fatalf("read-only script creation must preview its source (final=%t): %+v %t %v", final, files, recognized, err)
				}
			}
			if contents, err := os.ReadFile(target); err != nil || string(contents) != "old\n" {
				t.Fatalf("preview changed input: %q %v", contents, err)
			}
			if _, err := os.Stat(filepath.Join(directory, "report.py")); !os.IsNotExist(err) {
				t.Fatalf("preview created script: %v", err)
			}
		})
	}
}

func TestPythonOpenModesPreserveWriteScope(t *testing.T) {
	for _, tc := range []struct {
		script string
		write  bool
	}{
		{`open("target.txt")`, false},
		{`open("target.txt", "rb")`, false},
		{`open("target.txt", encoding="utf-8")`, false},
		{`open("target.txt", mode="r", encoding="utf-8")`, false},
		{`from pathlib import Path; Path("target.txt").open(encoding="utf-8")`, false},
		{"with open('target.txt') as f:\n    s = f.read()\ns = s.replace('old', 'new')", false},
		{`open("target.txt", "w")`, true},
		{`open("target.txt", "a")`, true},
		{`open("target.txt", "x")`, true},
		{`open("target.txt", "r+")`, true},
		{`open("target.txt", encoding="utf-8", mode="w")`, true},
		{`from pathlib import Path; Path("target.txt").open(encoding="utf-8", mode="w")`, true},
	} {
		t.Run(tc.script, func(t *testing.T) {
			directory := t.TempDir()
			result := execPythonScope(execProviderInput{identity: "python3", cwd: directory,
				args: []string{"-c", tc.script}, deadline: time.Now().Add(execProviderBudget), authoredOnly: true})
			if result.open || (len(result.scope) != 0) != tc.write {
				t.Fatalf("mode lost read/write distinction: %+v", result)
			}
			if tc.write && (len(result.scope) != 1 || len(result.scope[0].Operands) != 1 || result.scope[0].Operands[0].Path != filepath.Join(directory, "target.txt")) {
				t.Fatalf("writer target coverage regressed: %+v", result)
			}
		})
	}
}

func TestUISnapshotPythonReadOnlyScriptSource(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "target.txt"), "old\n")
	worker := liveDiffPreviewWorker{ctx: t.Context()}
	command := "cat >report.py <<'PY'\ns = open('target.txt').read()\ns = s.replace('old', 'new')\nprint(s)\nPY\n"
	files, recognized, err := worker.projectShell(command, directory, true)
	if err != nil || !recognized || len(files) != 1 {
		t.Fatalf("script source preview: %+v %t %v", files, recognized, err)
	}
	pane := diffview.PreviewPane{Prefer: "/root"}
	pane.Update(diffview.Preview{ID: "read-only", Workspace: directory, Caller: "/root", Status: diffview.PreviewEdit, Files: files})
	rows, err := pane.Render(t.Context(), directory, livediff.DarkTheme, 80, 12)
	if err != nil {
		t.Fatal(err)
	}
	uisnapshot.Assert(t, "testdata/snapshots/python-read-only-script-source.txt", strings.Join(rows, "\n")+"\n")
}
