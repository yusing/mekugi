package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/uisnapshot"
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

func TestUISnapshotNativeUIKeybindingsColumns(t *testing.T) {
	for _, width := range []int{40, 100, 140} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			frame := renderNativeKeybindings(width, 40)
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-keybindings-%d.txt", width), append(frame, "plain after shortcuts"), width)
		})
	}
}
