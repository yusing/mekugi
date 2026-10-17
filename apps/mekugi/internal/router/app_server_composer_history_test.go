package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestAppServerComposerUndoRedoGroupsTypingAndPaste(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "你好🙂")
	appServerTestKeys(t, u, "\x1b[200~a\rb\x1b[201~")
	if u.Text != "你好🙂a\nb" {
		t.Fatalf("draft = %q", u.Text)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.Text != "你好🙂" {
		t.Fatalf("undo paste = %q", u.Text)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.Text != "" {
		t.Fatalf("undo UTF-8 typing = %q", u.Text)
	}
	appServerTestKeys(t, u, "\x19\x19")
	if u.Text != "你好🙂a\nb" {
		t.Fatalf("redo groups = %q", u.Text)
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
	if u.Text != "" || len(u.Images) != 0 {
		t.Fatalf("image deletion = %q, %+v", u.Text, u.Images)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("undoable image file unavailable: %v", err)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.Text != "[Image 1]" || len(u.Images) != 1 || u.Images[0].Path != path {
		t.Fatalf("restored image = %q, %+v", u.Text, u.Images)
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
	if u.Text != "new" {
		t.Fatalf("redo after new edit = %q", u.Text)
	}
	appServerTestKeys(t, u, "\r")
	appServerTestKeys(t, u, "\x1a\x19")
	if u.Text != "" {
		t.Fatalf("submitted history restored into new draft: %q", u.Text)
	}
}

func TestAppServerComposerTokenIsOneNavigationUnit(t *testing.T) {
	for _, token := range []struct {
		name, label string
		attach      func(*testing.T, *appServerUI)
	}{
		{"image", "[Image 1]", func(t *testing.T, u *appServerUI) {
			u.attachImage(filepath.Join(t.TempDir(), "image.png"))
		}},
		{"skill", "$review", func(_ *testing.T, u *appServerUI) {
			start := len(u.Text)
			u.Text += "$review"
			u.Skills = append(u.Skills, composer.Token{Start: start, End: len(u.Text), Name: "review", Path: "/work/review/SKILL.md"})
		}},
		{"file", `@"file name.go"`, func(_ *testing.T, u *appServerUI) {
			bindComposerFile(u, `@"file name.go"`, "file name.go")
		}},
	} {
		t.Run(token.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			appServerTestKeys(t, u, "prefix ")
			token.attach(t, u)
			u.mainView.ComposerWidth = 16
			rows, _ := terminalsession.DraftLayout(u.Draft, u.mainView.ComposerWidth)
			if !strings.Contains(ansi.Strip(strings.Join(rows, "\n")), token.label) {
				t.Fatalf("split atomic token: %q", rows)
			}
			for _, tt := range []struct{ name, keys, want string }{
				{"left", "\x1b[D\x1b[DX", "aX" + token.label + "b"},
				{"right", "\x1b[H\x1b[C\x1b[CX", "a" + token.label + "Xb"},
				{"delete", "\x1b[D\x1b[D\x1b[3~", "ab"},
				{"ctrl left", "\x1b[1;5D\x1b[1;5DX", "aX" + token.label + "b"},
				{"ctrl right", "\x1b[H\x1b[1;5C\x1b[1;5CX", "a" + token.label + "Xb"},
				{"alt backspace", "\x1b[D\x1b\x7f", "ab"},
				{"alt delete", "\x1b[D\x1b[D\x1b[3;3~", "ab"},
				{"backspace undo", "\x1b[D\x7f\x1a", "a" + token.label + "b"},
			} {
				t.Run(tt.name, func(t *testing.T) {
					u, _ := newAppServerTestUI()
					appServerTestKeys(t, u, "a")
					token.attach(t, u)
					appServerTestKeys(t, u, "b"+tt.keys)
					if u.Text != tt.want {
						t.Fatalf("draft = %q, want %q", u.Text, tt.want)
					}
				})
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
	if _, err := screen.Write([]byte(strings.Join(first(u.testMainFrame(24, 8, 0)), "\r\n"))); err != nil {
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
		if u.Text != want {
			t.Fatalf("word undo = %q, want %q", u.Text, want)
		}
	}
	appServerTestKeys(t, u, "\x19\x19\x19\x7f\x7f\x7f\x7f\x7f")
	if u.Text != "hello big " {
		t.Fatalf("backspaces = %q", u.Text)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.Text != "hello big world" || u.Cursor() != len(u.Text) {
		t.Fatalf("backspace run undo = %q at %d", u.Text, u.Cursor())
	}
	appServerTestKeys(t, u, "\x1b[H\x1b[3~\x1b[3~\x1b[3~\x1a")
	if u.Text != "hello big world" {
		t.Fatalf("forward-delete run undo = %q", u.Text)
	}
	// A different kind of edit ends the run.
	appServerTestKeys(t, u, "\x1b[F\x7f\x7fX\x7f\x1a")
	if u.Text != "hello big worX" {
		t.Fatalf("mixed edits undo = %q", u.Text)
	}
}
