package launch

import (
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func composerSelectionScrollUI(t *testing.T, reverse bool) (*appServerUI, *vt.Emulator, []string) {
	t.Helper()
	u, _ := newAppServerTestUI()
	terminalTest(u).DiffOpen = false
	t.Cleanup(func() { terminalTest(u).Diff.Close() })
	var lines []string
	for i := range 20 {
		lines = append(lines, fmt.Sprintf("row %02d **你好**", i))
	}
	composerShellKeys(t, u, "\x1b[200~"+strings.Join(lines, "\n")+"\x1b[201~")
	if !reverse {
		u.CursorBack = len(u.Text)
	}
	screen := vt.NewEmulator(80, 20)
	t.Cleanup(func() { screen.Close() })
	if err := u.testPaint(screen, 80, 20); err != nil {
		t.Fatal(err)
	}
	if u.shell.Main.ComposerRect.H != 6 {
		t.Fatalf("composer viewport = %+v", u.shell.Main.ComposerRect)
	}
	return u, screen, lines
}

func TestComposerSelectionScrollBeyondViewport(t *testing.T) {
	for _, method := range []string{"edge", "wheel", "keys"} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%t", method, reverse), func(t *testing.T) {
				u, screen, lines := composerSelectionScrollUI(t, reverse)
				terminal := terminalTest(u)
				r := u.shell.Main.ComposerRect
				r.X += terminal.Layout.Main.X
				r.Y += terminal.Layout.Main.Y
				cursor, text := u.Cursor(), u.Text
				mouse := func(button, x, y int, release bool) {
					t.Helper()
					end := "M"
					if release {
						end = "m"
					}
					composerShellKeys(t, u, fmt.Sprintf("\x1b[<%d;%d;%d%s", button, x+1, y+1, end))
				}
				lastX := r.X + ansi.StringWidth(lines[19]) - 1
				startX, startY, endX, endY, target, wheel := r.X, r.Y, lastX, r.Y+r.H-1, 14, 65
				if reverse {
					startX, startY, endX, endY, target, wheel = lastX, r.Y+r.H-1, r.X, r.Y, 0, 64
				}
				mouse(0, startX, startY, false)
				s := terminal.Selection
				if s == nil || s.ScreenRows() == 0 || s.Top() != 14-target {
					t.Fatal("composer did not capture the full draft at its caret viewport")
				}
				if method == "keys" {
					for _, key := range []string{"\x1b[A", "\x1b[B", "\x1b[5~", "\x1b[6~", "\x1b[H", "\x1b[F"} {
						composerShellKeys(t, u, key)
					}
					if reverse {
						composerShellKeys(t, u, "\x1b[H")
					}
				} else {
					for attempts := 0; s.Top() != target && attempts < 20; attempts++ {
						if method == "wheel" {
							mouse(wheel, endX, endY, false)
						} else if reverse {
							mouse(32, endX, r.Y-1, false)
						} else {
							mouse(32, endX, r.Y+r.H, false)
						}
					}
				}
				if s.Top() != target {
					t.Fatalf("scroll top = %d, want %d", s.Top(), target)
				}
				mouse(32, endX, endY, false)
				mouse(0, endX, endY, true)
				if err := u.testPaint(screen, 80, 20); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(screen.String(), lines[target]) || s.Text() != text {
					t.Fatalf("scrolled source = %q, frame:\n%s", s.Text(), screen.String())
				}
				mouse(wheel^1, endX, endY, false)
				if terminal.Selection != s || s.Text() != text || s.Top() == target {
					t.Fatal("released wheel changed or dismissed the selected range")
				}
				composerShellKeys(t, u, "\x1b[H\x1b[F")
				if terminal.Selection != s || s.Text() != text || u.Text != text || u.Cursor() != cursor {
					t.Fatal("selection navigation changed the range, draft or caret")
				}
				composerShellKeys(t, u, "\x03")
				if terminal.Frame.Clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(text))+"\x07" || u.Text != text || u.Cursor() != cursor {
					t.Fatal("copy lost source or changed the draft/caret")
				}
				if err := u.testPaint(screen, 80, 20); err != nil {
					t.Fatal(err)
				}
				mouse(0, r.X, r.Y, false)
				mouse(32, lastX, r.Y, false)
				mouse(0, lastX, r.Y, true)
				if method == "edge" {
					if err := u.testPaint(screen, 60, 20); err != nil {
						t.Fatal(err)
					}
					if terminal.Selection != nil || u.Text != text || u.Cursor() != cursor {
						t.Fatal("resize did not dismiss selection and preserve the draft/caret")
					}
				} else {
					composerShellKeys(t, u, "x")
					if terminal.Selection != nil || u.Text != text[:cursor]+"x"+text[cursor:] {
						t.Fatal("editing did not dismiss selection at the preserved caret")
					}
				}
			})
		}
	}
}

func TestComposerSelectionRetainsDeliveredDraft(t *testing.T) {
	u, _, lines := composerSelectionScrollUI(t, true)
	reader, writer := io.Pipe()
	reader.Close()
	defer writer.Close()
	u.Text = strings.ReplaceAll(u.Text, "row", "new")
	if err := u.testPaint(writer, 80, 20); err == nil {
		t.Fatal("closed output accepted frame")
	}
	terminal := terminalTest(u)
	r := u.shell.Main.ComposerRect
	r.X += terminal.Layout.Main.X
	r.Y += terminal.Layout.Main.Y
	terminal.selectionMouse(0, r.X+ansi.StringWidth(lines[19])-1, r.Y+r.H-1, false)
	composerShellKeys(t, u, "\x1b[H")
	terminal.selectionMouse(0, r.X, r.Y, true)
	if terminal.Selection == nil || terminal.Selection.Text() != strings.Join(lines, "\n") {
		t.Fatal("failed write replaced frozen composer source")
	}
}

func TestUISnapshotComposerSelectionScrolled(t *testing.T) {
	u, screen, _ := composerSelectionScrollUI(t, true)
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	u.shell.Theme = diffrender.DarkTheme
	if err := u.testPaint(screen, 80, 20); err != nil {
		t.Fatal(err)
	}
	terminal := terminalTest(u)
	r := u.shell.Main.ComposerRect
	r.X += terminal.Layout.Main.X
	r.Y += terminal.Layout.Main.Y
	terminal.selectionMouse(0, r.X+15, r.Y+r.H-1, false)
	composerShellKeys(t, u, "\x1b[H")
	terminal.selectionMouse(0, r.X, r.Y, true)
	if err := u.testPaint(screen, 80, 20); err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(screen.Render(), "\n")[r.Y : r.Y+r.H]
	rows = append(rows, "plain after selection")
	uisnapshot.AssertTerminal(t, "testdata/snapshots/composer-selection-scrolled.txt", rows, 80)
}
