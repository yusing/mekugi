package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

func TestAppServerComposerUndoRedoGroupsTypingAndPaste(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "你好🙂")
	appServerTestKeys(t, u, "\x1b[200~a\rb\x1b[201~")
	if u.draft != "你好🙂a\nb" {
		t.Fatalf("draft = %q", u.draft)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.draft != "你好🙂" {
		t.Fatalf("undo paste = %q", u.draft)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.draft != "" {
		t.Fatalf("undo UTF-8 typing = %q", u.draft)
	}
	appServerTestKeys(t, u, "\x19\x19")
	if u.draft != "你好🙂a\nb" {
		t.Fatalf("redo groups = %q", u.draft)
	}
}

func TestAppServerComposerUndoImageDeletionKeepsFile(t *testing.T) {
	u, w := newAppServerTestUI()
	path := filepath.Join(t.TempDir(), "clipboard.png")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	u.attachImage(path)
	appServerTestKeys(t, u, "\x7f")
	if u.draft != "" || len(u.images) != 0 {
		t.Fatalf("image deletion = %q, %+v", u.draft, u.images)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("undoable image file unavailable: %v", err)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.draft != "[Image 1]" || len(u.images) != 1 || u.images[0].path != path {
		t.Fatalf("restored image = %q, %+v", u.draft, u.images)
	}
	appServerTestKeys(t, u, "\r")
	want := []composerWirePart{{Type: "localImage", Path: path}}
	if got := composerSubmittedParts(t, w); !composerPartsEqual(got, want) {
		t.Fatalf("restored wire = %+v", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("submitted image file unavailable: %v", err)
	}
}

func composerPartsEqual(got, want []composerWirePart) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestAppServerComposerRedoInvalidatedAndSubmitClearsHistory(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "abc\x1a")
	appServerTestKeys(t, u, "new\x19")
	if u.draft != "new" {
		t.Fatalf("redo after new edit = %q", u.draft)
	}
	appServerTestKeys(t, u, "\r")
	appServerTestKeys(t, u, "\x1a\x19")
	if u.draft != "" {
		t.Fatalf("submitted history restored into new draft: %q", u.draft)
	}
}

func TestAppServerComposerImageIsOneNavigationUnit(t *testing.T) {
	for _, tt := range []struct{ name, keys, want string }{
		{"left", "\x1b[D\x1b[DX", "aX[Image 1]b"},
		{"right", "\x1b[H\x1b[C\x1b[CX", "a[Image 1]Xb"},
		{"delete", "\x1b[D\x1b[D\x1b[3~", "ab"},
		{"ctrl left", "\x1b[1;5D\x1b[1;5DX", "aX[Image 1]b"},
		{"ctrl right", "\x1b[H\x1b[1;5C\x1b[1;5CX", "a[Image 1]Xb"},
		{"alt backspace", "\x1b[D\x1b\x7f", "ab"},
		{"alt delete", "\x1b[D\x1b[D\x1b[3;3~", "ab"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			appServerTestKeys(t, u, "a")
			u.attachImage(filepath.Join(t.TempDir(), "image.png"))
			appServerTestKeys(t, u, "b"+tt.keys)
			if u.draft != tt.want {
				t.Fatalf("draft = %q, want %q", u.draft, tt.want)
			}
		})
	}
}

func TestAppServerComposerImageTokenStyledAndCaretVisible(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.shell = &terminalUI{}
	appServerTestKeys(t, u, "a")
	u.attachImage(filepath.Join(t.TempDir(), "image.png"))
	appServerTestKeys(t, u, "b\x1b[D")
	screen := vt.NewEmulator(24, 8)
	t.Cleanup(func() { screen.Close() })
	if _, err := screen.Write([]byte(strings.Join(first(u.mainFrame(24, 8, 0)), "\r\n"))); err != nil {
		t.Fatal(err)
	}
	left, image, right := screen.CellAt(4, 6), screen.CellAt(5, 6), screen.CellAt(14, 6)
	if left == nil || image == nil || right == nil {
		t.Fatal("missing composer cells")
	}
	if image.Style.Attrs&uv.AttrBold == 0 || left.Style.Attrs&uv.AttrBold != 0 || right.Style.Attrs&uv.AttrBold != 0 {
		t.Fatal("image token not visually distinct from adjacent text")
	}
	if right.Style.Attrs&uv.AttrReverse == 0 {
		t.Fatal("caret next to image is not visible")
	}
}

func TestAppServerComposerUndoGroupsWordsAndDeletions(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "hello big world")
	for _, want := range []string{"hello big ", "hello ", ""} {
		appServerTestKeys(t, u, "\x1a")
		if u.draft != want {
			t.Fatalf("word undo = %q, want %q", u.draft, want)
		}
	}
	appServerTestKeys(t, u, "\x19\x19\x19\x7f\x7f\x7f\x7f\x7f")
	if u.draft != "hello big " {
		t.Fatalf("backspaces = %q", u.draft)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.draft != "hello big world" || u.cursor() != len(u.draft) {
		t.Fatalf("backspace run undo = %q at %d", u.draft, u.cursor())
	}
	appServerTestKeys(t, u, "\x1b[H\x1b[3~\x1b[3~\x1b[3~\x1a")
	if u.draft != "hello big world" {
		t.Fatalf("forward-delete run undo = %q", u.draft)
	}
	// A different kind of edit ends the run.
	appServerTestKeys(t, u, "\x1b[F\x7f\x7fX\x7f\x1a")
	if u.draft != "hello big worX" {
		t.Fatalf("mixed edits undo = %q", u.draft)
	}
}
