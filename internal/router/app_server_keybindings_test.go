package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestNativeUIKeybindings(t *testing.T) {
	for _, tc := range []struct {
		name, draft, input, want string
		focus                    int
		open                     bool
	}{
		{name: "empty", input: "?", open: true},
		{name: "toggle", input: "??"},
		{name: "typing dismisses", input: "?hello?", want: "hello?"},
		{name: "nonempty", draft: "hello", input: "?", want: "hello?"},
		{name: "whitespace", draft: " ", input: "?", want: " ?"},
		{name: "paste", input: "\x1b[200~?\x1b[201~", want: "?"},
		{name: "paste dismisses", input: "?\x1b[200~?\x1b[201~", want: "?"},
		{name: "activity focused", focus: 2, input: "?"},
		{name: "pane switch dismisses", input: "?\x023\x021"},
		{name: "mouse dismisses", input: "?\x1b[<0;5;5M"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.ensureShell()
			u.draft, u.shell.focus = tc.draft, tc.focus
			for _, key := range []byte(tc.input) {
				if err := u.shell.key(key); err != nil {
					t.Fatal(err)
				}
			}
			if u.draft != tc.want || u.keybindings != tc.open {
				t.Fatalf("draft=%q open=%v", u.draft, u.keybindings)
			}
		})
	}
}

func TestNativeUIKeybindingsPaintAndEscape(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	u.shell.side = false
	screen := vt.NewEmulator(100, 30)
	defer screen.Close()
	if err := u.shell.key('?'); err != nil {
		t.Fatal(err)
	}
	if err := u.paint(screen, 100, 30); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.String(), "Keyboard shortcuts") || !strings.Contains(screen.String(), "External editor") {
		t.Fatal(screen.String())
	}
	if u.mainContentPainted {
		t.Fatal("hidden transcript must not acknowledge journal presentation")
	}
	for _, size := range [][2]int{{1, 1}, {12, 5}, {40, 24}} {
		frame, _ := u.mainFrame(size[0], size[1], 0)
		if len(frame) != size[1] {
			t.Fatalf("height: %d", len(frame))
		}
		for _, row := range frame {
			if ansi.StringWidth(row) > size[0] {
				t.Fatalf("overflow: %q", row)
			}
		}
	}
	if err := u.shell.key(27); err != nil {
		t.Fatal(err)
	}
	u.shell.sequenceAt = time.Now().Add(-time.Second)
	if err := u.shell.flushEscape(); err != nil {
		t.Fatal(err)
	}
	if err := u.paint(screen, 100, 30); err != nil {
		t.Fatal(err)
	}
	if u.keybindings || strings.Contains(screen.String(), "Keyboard shortcuts") {
		t.Fatal("help not dismissed")
	}
	if !u.mainContentPainted {
		t.Fatal("visible transcript must allow journal presentation receipts")
	}
	if err := u.shell.key('?'); err != nil {
		t.Fatal(err)
	}
	if !u.keybindings || u.draft != "" {
		t.Fatal("cannot reopen after Escape")
	}
}

func TestNativeUIKeybindingsColumns(t *testing.T) {
	for _, width := range []int{40, 100, 140} {
		frame := renderNativeKeybindings(width, 40)
		plain := make([]string, len(frame))
		for i, row := range frame {
			plain[i] = ansi.Strip(row)
			if ansi.StringWidth(row) > width {
				t.Fatalf("width %d: overflowing row %q", width, row)
			}
		}
		text := strings.Join(plain, "\n")
		for _, label := range []string{"Compose", "Session", "Transcript", "External editor", "Close shortcuts"} {
			if !strings.Contains(text, label) {
				t.Fatalf("width %d: missing %s", width, label)
			}
		}
		if width == 140 {
			heading := -1
			for i, row := range plain {
				if strings.Contains(row, "Compose") {
					heading = i
					break
				}
			}
			if heading < 3 || !strings.Contains(plain[heading], "Session") || !strings.Contains(plain[heading], "Transcript") {
				t.Fatalf("headings do not share a row: %q", plain)
			}
			if !strings.Contains(frame[heading-2], "\x1b[1m") || !strings.Contains(frame[heading+1], "\x1b[38;2;80;155;225m") {
				t.Fatal("missing bold title or blue keys")
			}
			if strings.Index(plain[heading+1], "New line") != strings.Index(plain[heading+8], "External editor") {
				t.Fatal("Compose descriptions are not aligned")
			}
		}
		if !strings.Contains(plain[len(plain)-1], "Close shortcuts") {
			t.Fatal("shortcuts must dock at the bottom above the composer")
		}
	}
}
