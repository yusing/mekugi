package router

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestNativeFileEditorSelectionAndFilter(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	c := u.shell.diff
	c.SetWorkspace("/w")
	c.Update((presentation.Publication{}).With(presentation.GroupCaptures([]presentation.Chunk{
		liveDiffCapture("selected", "src/selected.go", 1, "old\n", "new\n", presentation.Origin{}),
		liveDiffCapture("other", "other.go", 2, "old\n", "new\n", presentation.Origin{}),
	})), "")
	u.shell.Focus = 1
	for _, key := range []byte("sg") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	// The folder cursor must not open the previously selected diff file.
	if err := u.shell.key('e'); err != nil {
		t.Fatal(err)
	}
	if err := u.shell.key('j'); err != nil {
		t.Fatal(err)
	}
	var request *openFileEditor
	if err := u.shell.key('e'); !errors.As(err, &request) || request.path != "/w/src/selected.go" {
		t.Fatalf("selected file request: %v", err)
	}
	for _, key := range []byte("/e") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if frame := strings.Join(paintSavedDiff(t, c, 120, 20), "\n"); !strings.Contains(frame, "/ e") {
		t.Fatalf("filter input missing:\n%s", frame)
	}
	u.shell.Focus = 0
	if err := u.shell.key('e'); err != nil || u.Text != "e" {
		t.Fatalf("composer input: %q %v", u.Text, err)
	}
}

func TestNativeFileEditorPathsAndFailure(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "selected.txt")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	t.Setenv("EDITOR", `sh -c 'printf changed > "$1"; exit 1' editor`)
	u.openSelectedFileEditor(path, output, output)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "changed" || !u.feedback.Alert {
		t.Fatalf("editor failure: data=%q notice=%q error=%v", data, u.feedback.Text, err)
	}
	t.Setenv("EDITOR", "true")
	for _, path := range []string{filepath.Join(dir, "missing"), dir, "relative-without-workspace"} {
		u.feedback.Text, u.feedback.Alert = "", false
		u.openSelectedFileEditor(path, output, output)
		if !u.feedback.Alert || !strings.HasPrefix(u.feedback.Text, "Open editor:") {
			t.Fatalf("path %q: %q", path, u.feedback.Text)
		}
	}
}
