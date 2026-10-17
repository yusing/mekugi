package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/modules/livepreview/projection"

	"github.com/yusing/mekugi/libraries/diffrender"

	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotPythonReadOnlyScriptSource(t *testing.T) {
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "target.txt"), "old\n")
	previewContext := t.Context()
	command := "cat >report.py <<'PY'\ns = open('target.txt').read()\ns = s.replace('old', 'new')\nprint(s)\nPY\n"
	files, _, recognized, err := projection.Shell(previewContext, command, directory, true)
	if err != nil || !recognized || len(files) != 1 {
		t.Fatalf("script source preview: %+v %t %v", files, recognized, err)
	}
	var pane previewview.PreviewPane
	pane.Update(previewmodel.Preview{ID: "read-only", Workspace: directory, Caller: "/root", Status: previewmodel.PreviewEdit, Files: files}, time.Now())
	rows, err := pane.RenderBatch(t.Context(), "/root", directory, diffrender.DarkTheme, 80, 16, 15)
	if err != nil {
		t.Fatal(err)
	}
	uisnapshot.Assert(t, "testdata/snapshots/python-read-only-script-source.txt", strings.Join(rows, "\n")+"\n")
}

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
			worker := struct{ ctx context.Context }{ctx: t.Context()}
			for _, final := range []bool{false, true} {
				command := "python3 - <<'PY'\n" + script + "PY\n"
				files, _, recognized, err := projection.Shell(worker.ctx, command, directory, final)
				if err != nil || recognized || len(files) != 0 {
					t.Fatalf("read-only execution invented an edit (final=%t): %+v %t %v", final, files, recognized, err)
				}
				if display := toolActivityShell(command); !strings.HasPrefix(display, "Run") {
					t.Fatalf("read-only execution displayed as edit: %s", display)
				}
				files, _, recognized, err = projection.Shell(worker.ctx, "cat >report.py <<'PY'\n"+script+"PY\n", directory, final)
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
