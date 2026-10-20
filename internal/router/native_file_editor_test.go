package router

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestNativeFileEditorSelectionAndFilter(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	c := u.shell.diff
	c.files = []livediff.File{navigationFile("src/selected.go"), navigationFile("other.go")}
	c.navigation.Rebuild(c.files, "")
	c.navigation.Focused = true
	u.shell.focus = 1
	// The folder cursor must not open the previously selected diff file.
	if err := u.shell.key('e'); err != nil {
		t.Fatal(err)
	}
	c.navigation.Cursor = navigationEntryIndex(c.navigation.Entries, "f:src/selected.go")
	var request *openFileEditor
	if err := u.shell.key('e'); !errors.As(err, &request) || request.path != "src/selected.go" {
		t.Fatalf("selected file request: %v", err)
	}
	c.navigation.Filtering = true
	if err := u.shell.key('e'); err != nil || c.navigation.Query != "e" {
		t.Fatalf("filter input: %q %v", c.navigation.Query, err)
	}
	u.shell.focus = 0
	if err := u.shell.key('e'); err != nil || u.draft != "e" {
		t.Fatalf("composer input: %q %v", u.draft, err)
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
	if err != nil || string(data) != "changed" || !u.noticeAlert {
		t.Fatalf("editor failure: data=%q notice=%q error=%v", data, u.notice, err)
	}
	t.Setenv("EDITOR", "true")
	for _, path := range []string{filepath.Join(dir, "missing"), dir, "relative-without-workspace"} {
		u.notice, u.noticeAlert = "", false
		u.openSelectedFileEditor(path, output, output)
		if !u.noticeAlert || !strings.HasPrefix(u.notice, "Open editor:") {
			t.Fatalf("path %q: %q", path, u.notice)
		}
	}
}
