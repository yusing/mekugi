package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/charmbracelet/x/ansi"
)

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
	connection, broker, _ := liveDiffTestBroker(t, store, previewmodel.Scope{
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
	preview := waitExecScopePreview(t, broker, func(preview previewmodel.Preview) bool {
		return preview.Status == previewmodel.PreviewEdit && len(preview.Files) == 1 &&
			strings.Contains(preview.Files[0].Diff, "+new old")
	})
	if preview.Input != "" || !strings.Contains(preview.Files[0].Diff, "-old old") {
		t.Fatalf("streamed Python body was not projected as a target diff: %+v", preview)
	}
	worker.appendDelta("s = s.replace('new',")
	retained := waitExecScopePreview(t, broker, func(preview previewmodel.Preview) bool {
		return !preview.Complete && preview.Status == previewmodel.PreviewEdit && len(preview.Files) == 1 &&
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
		return strings.Contains(plain, "○ M") && strings.Contains(plain, "target.txt") && strings.Contains(plain, "+new old")
	}))
	if strings.Contains(finalFrame, "cat >edit.py") || strings.Contains(finalFrame, "from pathlib import Path") ||
		strings.Contains(finalFrame, "s = s.replace") {
		t.Fatalf("terminal showed malformed script text instead of the retained diff: %q", finalFrame)
	}
	ui.quit(t)
}
