package router

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

func selectionTestUI(rows ...string) *terminalUI {
	main, _ := newAppServerTestUI()
	main.view.feedLeft, main.view.feedTop = 1, 1
	main.view.feedRight, main.view.feedRows = 40, len(rows)
	painted := append([]string(nil), rows...)
	painted = append(painted, "")
	return &terminalUI{main: main, width: 40, height: len(painted), paintedWidth: 40,
		paintedRows: painted, layout: terminalLayout{codex: terminalRect{0, 0, 40, len(rows)}}}
}

func selectionTestDrag(t *testing.T, u *terminalUI, x1, y1, x2, y2 int) {
	t.Helper()
	if !u.selectionMouse(0, x1, y1, false) || !u.selectionMouse(32, x2, y2, false) || !u.selectionMouse(0, x2, y2, true) {
		t.Fatal("selection did not consume drag and release")
	}
	if u.selection == nil || u.selection.dragging || !u.selection.moved {
		t.Fatal("release did not retain a completed selection")
	}
}

func TestTerminalUISelectionDragText(t *testing.T) {
	for _, tt := range []struct {
		name           string
		rows           []string
		x1, y1, x2, y2 int
		want           string
	}{
		{"forward", []string{"hello world"}, 0, 0, 4, 0, "hello"},
		{"reverse", []string{"hello world"}, 4, 0, 0, 0, "hello"},
		{"multiline", []string{"one two", "three four"}, 4, 0, 4, 1, "two\nthree"},
		{"reverse multiline", []string{"one two", "three four"}, 4, 1, 4, 0, "two\nthree"},
		{"wide styled", []string{"\x1b[31mA你好B\x1b[0m"}, 1, 0, 4, 0, "你好"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u := selectionTestUI(tt.rows...)
			selectionTestDrag(t, u, tt.x1, tt.y1, tt.x2, tt.y2)
			if got := u.selection.text(); got != tt.want {
				t.Fatalf("selected text = %q, want %q", got, tt.want)
			}
			rows := append([]string(nil), u.paintedRows...)
			u.paintSelection(rows)
			for _, label := range []string{"reference", "copy", "clear"} {
				if !strings.Contains(rows[len(rows)-1], label) {
					t.Fatalf("selection actions missing %s", label)
				}
			}
		})
	}
}

func TestTerminalUISelectionReferencePreservesDraftAndUndo(t *testing.T) {
	u := selectionTestUI("hello world")
	u.main.insertDraft("before after")
	u.main.cursorBack = len("after")
	u.focus = 2
	selectionTestDrag(t, u, 0, 0, 4, 0)
	if err := u.key('r'); err != nil {
		t.Fatal(err)
	}
	if u.main.draft != "before > hello\n\nafter" || u.focus != 0 || u.selection != nil {
		t.Fatalf("reference: draft=%q focus=%d selection=%v", u.main.draft, u.focus, u.selection)
	}
	u.main.undoDraft(false)
	if u.main.draft != "before after" || u.main.cursorBack != len("after") {
		t.Fatalf("undo reference lost draft/cursor: %q, %d", u.main.draft, u.main.cursorBack)
	}
}

func TestTerminalUISelectionCopyAndClear(t *testing.T) {
	for _, action := range []byte{'c', 3, 27} {
		t.Run(string(action), func(t *testing.T) {
			u := selectionTestUI("hello world")
			u.main.draft = "keep me"
			selectionTestDrag(t, u, 0, 0, 4, 0)
			if err := u.key(action); err != nil {
				t.Fatal(err)
			}
			if action == 27 {
				// A lone Escape clears only after the parser has ruled out a CSI sequence.
				if u.selection == nil {
					t.Fatal("Escape cleared selection before sequence timeout")
				}
				u.sequenceAt = time.Now().Add(-time.Second)
				if err := u.flushEscape(); err != nil {
					t.Fatal(err)
				}
			}
			want := ""
			if action == 'c' || action == 3 {
				want = "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hello")) + "\x07"
			}
			if u.selection != nil || u.main.draft != "keep me" || u.clipboard != want {
				t.Fatalf("action %q: selection=%v draft=%q clipboard=%q", action, u.selection, u.main.draft, u.clipboard)
			}
		})
	}
}

func TestTerminalUISelectionLinkClickVersusDrag(t *testing.T) {
	for _, tt := range []struct{ link, want string }{
		{"https://example.com/docs?q=hello#section", "https://example.com/docs?q=hello#section"},
		{"file:///tmp/my%20project/main.go", "/tmp/my project/main.go"},
	} {
		t.Run(tt.link, func(t *testing.T) {
			row := "\x1b]8;;" + tt.link + "\x1b\\docs\x1b]8;;\x1b\\"
			u := selectionTestUI(row)
			if !u.selectionMouse(0, 1, 0, false) || !u.selectionMouse(0, 1, 0, true) {
				t.Fatal("link click unhandled")
			}
			want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(tt.want)) + "\x07"
			if u.selection != nil || u.clipboard != want {
				t.Fatalf("click: selection=%v clipboard=%q", u.selection, u.clipboard)
			}
			u = selectionTestUI(row)
			selectionTestDrag(t, u, 0, 0, 3, 0)
			if u.clipboard != "" || u.selection.text() != "docs" {
				t.Fatalf("drag copied link or lost text: clipboard=%q text=%q", u.clipboard, u.selection.text())
			}
		})
	}
}

func TestTerminalUISelectionResizeClears(t *testing.T) {
	for _, width := range []bool{false, true} {
		u := selectionTestUI("hello world")
		selectionTestDrag(t, u, 0, 0, 4, 0)
		rows := append([]string(nil), u.paintedRows...)
		if width {
			u.width++
		} else {
			rows = append(rows, "")
		}
		u.paintSelection(rows)
		if u.selection != nil {
			t.Fatal("resize retained stale selection coordinates")
		}
	}
}

func TestTerminalUISelectionReplacementKeepsCleanLinks(t *testing.T) {
	u := selectionTestUI("\x1b]8;;https://example.com\x1b\\report\x1b]8;;\x1b\\ other")
	selectionTestDrag(t, u, 0, 0, 5, 0)
	u.paintSelection(u.paintedRows)
	u.selectionMouse(0, 2, 0, false)
	u.selectionMouse(0, 2, 0, true)
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("https://example.com")) + "\x07"
	if u.clipboard != want {
		t.Fatalf("reselected link lost its target: %q", u.clipboard)
	}

	u = selectionTestUI("first second")
	selectionTestDrag(t, u, 0, 0, 4, 0)
	u.paintSelection(u.paintedRows)
	selectionTestDrag(t, u, 6, 0, 11, 0)
	if strings.Contains(u.selection.rows[0], "\x1b[7m") || u.selection.text() != "second" {
		t.Fatalf("replacement inherited old highlight: %q", u.selection.rows[0])
	}
}

func TestTerminalUISelectionKeepsPressedHitTarget(t *testing.T) {
	for _, question := range []bool{false, true} {
		u := selectionTestUI("click target")
		old, next := liveActivitySnippet{run: 1}, liveActivitySnippet{run: 2}
		if question {
			u.main.view.feedQuestions = []uint64{1}
			u.main.view.questionRows = map[uint64]int{1: 3, 2: 9}
		} else {
			u.main.view.feedSnippets = []liveActivitySnippet{old}
		}
		u.selectionMouse(0, 0, 0, false)
		// A streaming repaint replaces viewport hit maps before button release.
		u.main.view.feedQuestions = []uint64{2}
		u.main.view.feedSnippets = []liveActivitySnippet{next}
		u.paintSelection(u.paintedRows)
		u.selectionMouse(0, 0, 0, true)
		if question {
			if u.main.view.flashQuestion != 1 || u.main.view.offset != 3 {
				t.Fatal("clicked an advancing question target")
			}
		} else if !u.main.view.expanded[old] || u.main.view.expanded[next] {
			t.Fatal("clicked an advancing snippet target")
		}
	}
}

func TestTerminalUISelectionRenderedClipboard(t *testing.T) {
	for _, activity := range []bool{false, true} {
		u, _ := newAppServerTestUI()
		u.ensureShell()
		view := u.view
		if activity {
			view = u.agents
			u.shell.diffOpen = false
		}
		view.applyAppServerItem("main", "main", "turn", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: "hello [report](</tmp/my project/report.go:12>)"})
		screen := vt.NewEmulator(120, 30)
		defer screen.Close()
		var wire bytes.Buffer
		paint := func() {
			t.Helper()
			if err := u.paint(io.MultiWriter(screen, &wire), 120, 30); err != nil {
				t.Fatal(err)
			}
		}
		paint()
		x, y := -1, -1
		for row := range 30 {
			for col := range 120 {
				if c := screen.CellAt(col, row); c != nil && c.Link.URL != "" {
					x, y = col, row
					break
				}
			}
			if x >= 0 {
				break
			}
		}
		if x < 0 {
			t.Fatalf("no rendered link (activity=%v): %s", activity, screen.String())
		}
		for _, end := range []string{"M", "m"} {
			if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", x+1, y+1, end)); err != nil {
				t.Fatal(err)
			}
		}
		wire.Reset()
		paint()
		want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("/tmp/my project/report.go:12")) + "\x07"
		if !strings.Contains(wire.String(), want) {
			t.Fatalf("clipboard request missing: %q", wire.String())
		}
		wire.Reset()
		paint()
		if strings.Contains(wire.String(), "\x1b]52;") {
			t.Fatal("clipboard replayed on repaint")
		}
		for _, event := range []string{fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1), fmt.Sprintf("\x1b[<32;%d;%dM", x+6, y+1), fmt.Sprintf("\x1b[<0;%d;%dm", x+6, y+1)} {
			if err := u.shell.mouse(event); err != nil {
				t.Fatal(err)
			}
		}
		paint()
		if !strings.Contains(screen.String(), "r reference · ctrl+c copy · esc clear") {
			t.Fatalf("missing menu: %s", screen.String())
		}
		if err := u.shell.mouse("\x1b[<0;2;30M"); err != nil {
			t.Fatal(err)
		}
		if u.draft != "> report\n\n" {
			t.Fatalf("reference=%q", u.draft)
		}
	}
}

func TestTerminalUISelectionComposer(t *testing.T) {
	for _, size := range [][2]int{{120, 30}, {40, 12}, {10, 4}} {
		u, _ := newAppServerTestUI()
		u.ensureShell()
		u.insertDraft("你好abc\nsecond")
		screen := vt.NewEmulator(size[0], size[1])
		defer screen.Close()
		if err := u.paint(screen, size[0], size[1]); err != nil {
			t.Fatal(err)
		}
		r := u.composerRect
		r.x += u.shell.layout.codex.x
		r.y += u.shell.layout.codex.y
		selectionTestDrag(t, u.shell, r.x, r.y, r.x+3, r.y)
		if got := u.shell.selection.text(); got != "你好" {
			t.Fatalf("size %v: selected %q", size, got)
		}
		if err := u.shell.key(3); err != nil {
			t.Fatal(err)
		}
		if u.draft != "你好abc\nsecond" || u.quitRequested {
			t.Fatal("copy changed draft or quit")
		}
		want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("你好")) + "\x07"
		if u.shell.clipboard != want {
			t.Fatalf("clipboard %q", u.shell.clipboard)
		}
	}
}

func TestTerminalHintsRenderingAndHitRegions(t *testing.T) {
	hints := terminalHints{{"ctrl+c", "copy", 'c'}, {"esc", "clear", 27}}
	if got := hints.render(); got != "\x1b[1mctrl+c\x1b[22m copy · \x1b[1mesc\x1b[22m clear" {
		t.Fatalf("render %q", got)
	}
	for _, tc := range []struct {
		x, width int
		want     byte
	}{{0, 40, 'c'}, {10, 40, 'c'}, {11, 40, 0}, {14, 40, 27}, {14, 16, 0}} {
		if got := hints.actionAt(tc.x, tc.width); got != tc.want {
			t.Fatalf("%+v: %d", tc, got)
		}
	}
}

func TestTerminalUISelectionComposerReplacesTranscript(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	screen := vt.NewEmulator(40, 8)
	defer screen.Close()
	paint := func() {
		t.Helper()
		if err := u.paint(screen, 40, 8); err != nil {
			t.Fatal(err)
		}
	}
	paint()
	u.view.feedQuestions = []uint64{1, 1, 1, 1}
	u.view.questionRows = map[uint64]int{1: 99}
	u.insertDraft("first\nsecond\nthird\nfourth\nfifth")
	paint()
	r := u.composerRect
	r.x += u.shell.layout.codex.x
	r.y += u.shell.layout.codex.y
	selectionTestDrag(t, u.shell, r.x, r.y, r.x+5, r.y+1)
	if got := u.shell.selection.text(); got != "first\nsecond" {
		t.Fatalf("selected %q", got)
	}
	u.shell.selectionMouse(0, r.x, r.y, false)
	u.shell.selectionMouse(0, r.x, r.y, true)
	if u.view.flashQuestion != 0 {
		t.Fatal("composer click activated stale transcript question")
	}
}
