package router

import (
	"bytes"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

func TestAppServerComposerEditingKeys(t *testing.T) {
	for _, tt := range []struct{ name, keys, want string }{
		{"left insertion", "ac\x1b[Db", "abc"},
		{"right insertion", "abcd\x1b[D\x1b[D\x1b[CX", "abcXd"},
		{"SS3 arrows", "ac\x1bODb\x1bOCd", "abcd"},
		{"backspace at cursor", "abcd\x1b[D\x7f", "abd"},
		{"delete at cursor", "abcd\x1b[D\x1b[3~", "abc"},
		{"grapheme left", "ae\u0301👩‍💻z\x1b[D\x1b[DX", "ae\u0301X👩‍💻z"},
		{"grapheme backspace", "ae\u0301👩‍💻z\x1b[D\x7f", "ae\u0301z"},
		{"grapheme delete", "ae\u0301z\x1b[D\x1b[D\x1b[3~", "az"},
		{"ctrl left", "one  two three\x1b[1;5D\x1b[1;5DX", "one  Xtwo three"},
		{"ctrl right", "one  two three\x1b[H\x1b[1;5CX", "oneX  two three"},
		{"comma left", "abc,def\x1b[1;3DX", "abc,Xdef"},
		{"comma right", "abc,def\x1b[H\x1b[1;3CX", "abcX,def"},
		{"comma right twice", "abc,def\x1b[H\x1b[1;3C\x1b[1;3CX", "abc,defX"},
		{"ordinary meta letters", "before bf", "before bf"},
		{"pasted meta letters are text", "one two\x1b[200~\x1bb\x1bf\x1b[201~X", "one twobfX"},
		{"pasted alt arrows do not move", "one two\x1b[200~\x1b[1;3D\x1b[1;3C\x1b[201~X", "one twoX"},
		{"left boundary", "a\x1b[D\x1b[D\x7fX", "Xa"},
		{"right boundary", "a\x1b[C\x1b[3~X", "aX"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, w := newAppServerTestUI()
			appServerTestKeys(t, u, tt.keys)
			if u.Text != tt.want || w.Len() != 0 {
				t.Fatalf("draft = %q, want %q; unexpected wire = %q", u.Text, tt.want, w.String())
			}
		})
	}
}

func TestAppServerComposerVerticalEditing(t *testing.T) {
	for _, tt := range []struct{ name, draft, keys, want string }{
		{"multiline up", "abcd\nxy", "\x1b[AX", "abXcd\nxy"},
		{"multiline down", "abcd\nxyz", "\x1b[A\x1b[D\x1b[BX", "abcd\nxyXz"},
		{"wrapped up", "12345678901abc", "\x1b[AX", "123X45678901abc"},
		{"wrapped down", "12345678901abc", "\x1b[A\x1b[D\x1b[BX", "12345678901abXc"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			appServerTestKeys(t, u, tt.draft)
			u.testMainFrame(16, 10, 0) // Eleven display cells remain after borders and prompt.
			appServerTestKeys(t, u, tt.keys)
			if u.Text != tt.want {
				t.Fatalf("draft = %q, want %q", u.Text, tt.want)
			}
		})
	}
}

type composerWirePart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Path string `json:"path,omitempty"`
}

func composerSubmittedParts(t *testing.T, w *appServerTestInput) []composerWirePart {
	t.Helper()
	var request struct {
		Method string `json:"method"`
		Params struct {
			Input []composerWirePart `json:"input"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(w.Bytes()), &request); err != nil {
		t.Fatal(err)
	}
	if request.Method != "turn/start" {
		t.Fatalf("method = %q", request.Method)
	}
	return request.Params.Input
}

func TestAppServerComposerImagesOrderedOnWire(t *testing.T) {
	u, w := newAppServerTestUI()
	path1, path2 := filepath.Join(t.TempDir(), "one.png"), filepath.Join(t.TempDir(), "two.png")
	appServerTestKeys(t, u, "before ")
	attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, path1)
	appServerTestKeys(t, u, " after\x1b[H")
	attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, path2)
	if u.Text != "[Image 1]before [Image 2] after" {
		t.Fatalf("draft = %q", u.Text)
	}
	appServerTestKeys(t, u, "\r")
	want := []composerWirePart{{Type: "localImage", Path: path2}, {Type: "text", Text: "before "}, {Type: "localImage", Path: path1}, {Type: "text", Text: " after"}}
	if got := composerSubmittedParts(t, w); !reflect.DeepEqual(got, want) {
		t.Fatalf("parts = %+v, want %+v", got, want)
	}
}

func TestAppServerComposerImageTokenAtomic(t *testing.T) {
	for _, keys := range []string{"\x7f", "\x1b[D\x1b[3~"} {
		u, w := newAppServerTestUI()
		path := filepath.Join(t.TempDir(), "clipboard.png")
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		appServerTestKeys(t, u, "before ")
		attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, path)
		appServerTestKeys(t, u, "\x1b[DX\x1b[C")
		if u.Text != "before X[Image 1]" {
			t.Fatalf("arrow split image token: %q", u.Text)
		}
		appServerTestKeys(t, u, keys)
		if u.Text != "before X" {
			t.Fatalf("deletion split token: %q", u.Text)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("undoable image was removed: %v", err)
		}
		appServerTestKeys(t, u, "\r")
		want := []composerWirePart{{Type: "text", Text: "before X"}}
		if got := composerSubmittedParts(t, w); !reflect.DeepEqual(got, want) {
			t.Fatalf("deleted image still sent: %+v", got)
		}
	}
}

func TestAppServerComposerRejectedImagesRestored(t *testing.T) {
	u, w := newAppServerTestUI()
	path1, path2 := filepath.Join(t.TempDir(), "one.png"), filepath.Join(t.TempDir(), "two.png")
	appServerTestKeys(t, u, "first ")
	attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, path1)
	appServerTestKeys(t, u, "\rnext ")
	attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, path2)
	appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"rejected"}}`)
	if u.Text != "first [Image 1]\nnext [Image 2]" {
		t.Fatalf("restored draft = %q", u.Text)
	}
	w.Reset()
	appServerTestKeys(t, u, "\r")
	want := []composerWirePart{{Type: "text", Text: "first "}, {Type: "localImage", Path: path1}, {Type: "text", Text: "\nnext "}, {Type: "localImage", Path: path2}}
	if got := composerSubmittedParts(t, w); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored parts = %+v, want %+v", got, want)
	}
}

func TestAppServerComposerImageEchoReconcilesPending(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "before ")
	attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, filepath.Join(t.TempDir(), "image.png"))
	appServerTestKeys(t, u, " after\r")
	if len(u.attached.source.Entries) != 1 || u.attached.source.Entries[0].Source.Text != "before [Image 1] after" {
		t.Fatalf("missing immediate image echo: %+v", u.attached.source.Entries)
	}
	for _, method := range []string{"item/started", "item/completed"} {
		appServerTestMessage(t, u, `{"method":"`+method+`","params":{"threadId":"main","turnId":"t","item":{"id":"user-1","type":"userMessage","content":[{"type":"text","text":"before "},{"type":"localImage"},{"type":"text","text":" after"}]}}}`)
	}
	if len(u.attached.source.Entries) != 1 || u.attached.source.Entries[0].Source.Text != "before [Image 1] after" || u.attached.source.Entries[0].Source.Native.Item != "user-1" {
		t.Fatalf("authoritative image echo duplicated or lost placeholder: %+v", u.attached.source.Entries)
	}
}

func TestAppServerComposerMovedCaretVisible(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "abc\x1b[D\x1b[D")
	screen := vt.NewEmulator(16, 8)
	t.Cleanup(func() { screen.Close() })
	if _, err := screen.Write([]byte(strings.Join(first(u.testMainFrame(16, 8, 0)), "\r\n"))); err != nil {
		t.Fatal(err)
	}
	carets := 0
	for y := range 8 {
		for x := range 16 {
			cell := screen.CellAt(x, y)
			if cell != nil && cell.Style.Attrs&uv.AttrReverse != 0 {
				carets++
				if x != 5 || y != 6 {
					t.Fatalf("caret at (%d,%d), want (5,6)", x, y)
				}
			}
		}
	}
	if carets != 1 {
		t.Fatalf("visible caret count = %d", carets)
	}
}

func TestAppServerComposerFullLineNewlineCaret(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "12345678901\nx\x1b[H\x1b[D")
	screen := vt.NewEmulator(16, 10)
	defer screen.Close()
	frame, _ := u.testMainFrame(16, 10, 0)
	if _, err := screen.Write([]byte(strings.Join(frame, "\r\n"))); err != nil {
		t.Fatal(err)
	}
	carets := 0
	for y := range 10 {
		for x := range 16 {
			cell := screen.CellAt(x, y)
			if cell != nil && cell.Style.Attrs&uv.AttrReverse != 0 {
				carets++
				if x >= 15 {
					t.Fatalf("caret overlaps border at %d,%d", x, y)
				}
			}
		}
	}
	if carets != 1 || u.Cursor() != 11 {
		t.Fatalf("caret count=%d, offset=%d", carets, u.Cursor())
	}
	appServerTestKeys(t, u, "Z")
	if u.Text != "12345678901Z\nx" {
		t.Fatalf("draft=%q", u.Text)
	}
}

func TestAppServerComposerImageAdjacentCombiningMark(t *testing.T) {
	u, _ := newAppServerTestUI()
	path := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, path)
	appServerTestKeys(t, u, "\u0301\x7f")
	if u.Text != "[Image 1]" || len(u.Images) != 1 {
		t.Fatalf("mark deletion broke attachment: %q, %v", u.Text, u.Images)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	appServerTestKeys(t, u, "\x7f")
	if u.Text != "" || len(u.Images) != 0 {
		t.Fatalf("attachment deletion=%q, %v", u.Text, u.Images)
	}
}

func TestAppServerComposerAltWordDeletion(t *testing.T) {
	for _, tt := range []struct{ name, keys, want string }{
		{"comma backward", "abc,def\x1b\x7f", "abc,"},
		{"comma forward", "abc,def\x1b[H\x1b[3;3~", ",def"},
		{"comma preceding backward", "abc,\x1b\x7f", ""},
		{"alt backspace DEL", "one two\x1b\x7f", "one "},
		{"alt backspace BS", "one two\x1b\x08", "one "},
		{"alt backspace trailing whitespace", "one two  \x1b\x7f", "one "},
		{"alt backspace CSI u", "one two\x1b[127;3u", "one "},
		{"alt backspace CSI u BS", "one two\x1b[8;3u", "one "},
		{"alt delete", "one two three\x1b[H\x1b[1;5C\x1b[3;3~", "one three"},
		{"unicode previous word", "one 你好👩‍💻\x1b\x7f", "one "},
		{"unicode next word", "你好👩‍💻 next\x1b[H\x1b[3;3~", " next"},
		{"start boundary", "one\x1b[H\x1b\x7f", "one"},
		{"end boundary", "one\x1b[3;3~", "one"},
		{"paste is not shortcut", "one\x1b[200~\x1b[3;3~\x1b\x7f\x1b[201~", "one\x7f"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			appServerTestKeys(t, u, tt.keys)
			if u.Text != tt.want {
				t.Fatalf("draft=%q, want %q", u.Text, tt.want)
			}
		})
	}
	for _, keys := range []string{"\x1b\x7f", "\x1b[D\x1b[3;3~"} {
		u, _ := newAppServerTestUI()
		appServerTestKeys(t, u, "before ")
		attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, filepath.Join(t.TempDir(), "image.png"))
		appServerTestKeys(t, u, keys)
		if u.Text != "before " || len(u.Images) != 0 {
			t.Fatalf("word deletion split image: %q, %v", u.Text, u.Images)
		}
	}
}

// Exercise the terminal router as well as the composer: remote input arrives
// byte-by-byte and Option arrows may be Meta letters or modified CSI arrows.
func TestAppServerComposerOptionWordNavigation(t *testing.T) {
	for _, encoding := range []struct{ name, left, right string }{
		{"meta", "\x1bb", "\x1bf"},
		{"alt arrows", "\x1b[1;3D", "\x1b[1;3C"},
		{"ctrl arrows", "\x1b[1;5D", "\x1b[1;5C"},
	} {
		t.Run(encoding.name, func(t *testing.T) {
			u, wire := newAppServerTestUI()

			keys := func(input string) {
				t.Helper()
				for _, key := range []byte(input) {
					if err := terminalTest(u).key(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			keys("one  你好👩‍💻 three")
			keys(encoding.left + encoding.left + "X")
			if u.Text != "one  X你好👩‍💻 three" {
				t.Fatalf("backward: %q", u.Text)
			}
			keys(encoding.right + "Y")
			if u.Text != "one  X你好👩‍💻Y three" {
				t.Fatalf("forward: %q", u.Text)
			}
			keys(strings.Repeat(encoding.left, 5))
			if u.Cursor() != 0 {
				t.Fatalf("start boundary: %d", u.Cursor())
			}
			keys(strings.Repeat(encoding.right, 5))
			if u.Cursor() != len(u.Text) {
				t.Fatalf("end boundary: %d", u.Cursor())
			}
			attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, filepath.Join(t.TempDir(), "image.png"))
			end := u.Cursor()
			keys(encoding.left)
			if u.Cursor() != end-len("[Image 1]") {
				t.Fatalf("split image moving left: %d", u.Cursor())
			}
			keys(encoding.right)
			if u.Cursor() != end || len(u.Images) != 1 {
				t.Fatal("split image moving right")
			}
			if wire.Len() != 0 {
				t.Fatalf("navigation submitted input: %s", wire.String())
			}
			screen := vt.NewEmulator(100, 30)
			defer screen.Close()
			if err := u.testPaint(screen, 100, 30); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(screen.String(), "X你好👩‍💻Y three") {
				t.Fatalf("edited draft missing from frame:\n%s", screen.String())
			}
		})
	}
}

func TestAppServerComposerKillShortcutsThroughTerminal(t *testing.T) {
	for _, tt := range []struct{ name, draft, movement, key, want string }{
		{"option DEL", "one two", "", "\x1b\x7f", "one "},
		{"option BS", "one two", "", "\x1b\x08", "one "},
		{"option CSI", "one two", "", "\x1b[127;3u", "one "},
		{"macOS ctrl W", "one 你好👩‍💻  ", "", "\x17", "one "},
		{"word middle", "one two three", "\x1b[1;5D", "\x17", "one three"},
		{"word start", "one", "\x1b[H", "\x17", "one"},
		{"kill suffix", "abc你好👩‍💻\nnext", "\x1b[H\x1b[D\x1b[D\x1b[D\x1b[D", "\x0b", "abc\nnext"},
		{"kill newline", "one\ntwo", "\x1b[H\x1b[D", "\x0b", "onetwo"},
		{"kill last line", "one two", "\x1b[1;5D", "\x0b", "one "},
		{"kill end", "one", "", "\x0b", "one"},
		{"empty", "", "", "\x0b", ""},
		{"paste ignores controls", "one two", "", "\x1b[200~\x17\x0b\x1b[201~", "one two"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, wire := newAppServerTestUI()

			keys := func(s string) {
				t.Helper()
				for _, b := range []byte(s) {
					if err := terminalTest(u).key(b); err != nil {
						t.Fatal(err)
					}
				}
			}
			keys(tt.draft)
			keys(tt.movement)
			at := u.Cursor()
			keys(tt.key)
			if u.Text != tt.want {
				t.Fatalf("draft=%q, want %q", u.Text, tt.want)
			}
			if tt.want != tt.draft {
				keys("\x1a")
				if u.Text != tt.draft || u.Cursor() != at {
					t.Fatalf("undo=%q at %d", u.Text, u.Cursor())
				}
				keys("\x19")
				if u.Text != tt.want {
					t.Fatalf("redo=%q", u.Text)
				}
			}
			if wire.Len() != 0 {
				t.Fatalf("shortcut submitted: %s", wire.String())
			}
		})
	}
	for _, key := range []string{"\x17", "\x0b"} {
		u, _ := newAppServerTestUI()
		appServerTestKeys(t, u, "before ")
		attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, filepath.Join(t.TempDir(), "image.png"))
		if key == "\x0b" {
			appServerTestKeys(t, u, "\x1b[D")
		}
		appServerTestKeys(t, u, key)
		if u.Text != "before " || len(u.Images) != 0 {
			t.Fatalf("split attachment: %q", u.Text)
		}
		appServerTestKeys(t, u, "\x1a")
		if u.Text != "before [Image 1]" || len(u.Images) != 1 {
			t.Fatalf("attachment undo: %q", u.Text)
		}
	}
}

func TestAppServerComposerKeycapWordBoundaries(t *testing.T) {
	for _, emoji := range []string{"#️⃣", "*️⃣"} {
		for _, tt := range []struct{ keys, want string }{
			{emoji + "\x1b[1;3DX", "X" + emoji},
			{emoji + "\x1b[H\x1b[1;3CX", emoji + "X"},
			{emoji + "\x1b\x7f", ""},
			{emoji + "\x1b[H\x1b[3;3~", ""},
		} {
			u, _ := newAppServerTestUI()
			appServerTestKeys(t, u, tt.keys)
			if u.Text != tt.want {
				t.Fatalf("keys %q: got %q, want %q", tt.keys, u.Text, tt.want)
			}
		}
	}
}
