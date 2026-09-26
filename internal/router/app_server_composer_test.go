package router

import (
	"slices"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func appServerComposerScreenText(t *testing.T, frame []byte, width, height int) string {
	t.Helper()
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	if _, err := screen.Write(frame); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for row := range strings.SplitSeq(screen.String(), "\n") {
		inner := width
		if strings.HasPrefix(row, "││") {
			row, inner = ansi.Cut(row, 1, width-1), width-2 // Inside the Main pane frame.
		}
		if !strings.HasPrefix(row, "│ ") {
			continue
		}
		// Exclude the border and prompt columns, preserving whitespace across wraps.
		text.WriteString(ansi.Cut(row, 4, inner-1))
	}
	return strings.TrimRight(text.String(), " ")
}

func TestAppServerComposerFrame(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "first\n你好")
	frame, _ := u.mainFrame(16, 8, 0)
	for i := range frame {
		frame[i] = ansi.Strip(frame[i])
	}
	want := []string{
		"╭──────────────╮",
		"│ ❯ first      │",
		"│   你好       │",
		"╰──────────────╯",
	}
	if len(frame) != 8 || !slices.Equal(frame[len(frame)-len(want):], want) {
		t.Fatalf("composer frame = %q, want final rows %q in 8-row frame", frame, want)
	}
	if u.draft != "first\n你好" {
		t.Fatalf("render mutated draft: %q", u.draft)
	}
}

func TestAppServerComposerResizeKeepsDraftTail(t *testing.T) {
	for _, size := range []struct {
		width, height, composerRows int
	}{{20, 8, 6}, {7, 6, 6}, {7, 3, 3}, {6, 2, 2}, {2, 1, 1}, {1, 1, 1}} {
		u, _ := newAppServerTestUI()
		u.draft = "old\nlines\nmore\n你好\nlast\nZ"
		frame, _ := u.mainFrame(size.width, size.height, 0)
		if len(frame) != size.height {
			t.Fatalf("size %v: got %d rows", size, len(frame))
		}
		for _, row := range frame[len(frame)-size.composerRows:] {
			if ansi.StringWidth(row) > size.width {
				t.Fatalf("size %v: overflowing row %q", size, row)
			}
		}
		if !strings.Contains(strings.Join(frame, "\n"), "Z") {
			t.Fatalf("size %v: draft tail missing: %q", size, frame)
		}
	}
}

func TestAppServerComposerObservedModelCaption(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.requests["1"] = "thread/start"
	appServerTestMessage(t, u, `{"id":1,"result":{"thread":{"id":"main"},"model":"actual-model","reasoningEffort":"high"}}`)
	frame, _ := u.mainFrame(70, 8, 0)
	bottom := ansi.Strip(frame[len(frame)-1])
	if !strings.HasPrefix(bottom, "╰───") || !strings.HasSuffix(bottom, " actual-model (high) ─╯") || ansi.StringWidth(bottom) != 70 {
		t.Fatalf("observed model caption: %q", bottom)
	}
	u.requests["2"] = "thread/start"
	appServerTestMessage(t, u, `{"id":2,"result":{"thread":{"id":"new"},"reasoningEffort":null}}`)
	frame, _ = u.mainFrame(70, 8, 0)
	bottom = ansi.Strip(frame[len(frame)-1])
	if bottom != "╰"+strings.Repeat("─", 68)+"╯" {
		t.Fatalf("unknown model retained stale caption: %q", bottom)
	}
}

func TestAppServerComposerCaretFollowsFocusAndWrap(t *testing.T) {
	for _, focus := range []int{0, 1} {
		u, _ := newAppServerTestUI()
		u.shell = &terminalUI{focus: focus}
		u.draft = "12345678901" // Fills the 11-column input, placing its caret on the next row.
		screen := vt.NewEmulator(16, 8)
		t.Cleanup(func() { screen.Close() })
		if _, err := screen.Write([]byte(strings.Join(first(u.mainFrame(16, 8, 0)), "\r\n"))); err != nil {
			t.Fatal(err)
		}
		for y := 4; y < 8; y++ {
			for x := range 16 {
				cell := screen.CellAt(x, y)
				if cell == nil {
					t.Fatalf("missing composer cell at (%d, %d)", x, y)
				}
				wantCaret := focus == 0 && x == 4 && y == 6
				if cell.Style.Bg != nil {
					t.Fatalf("cell (%d, %d) must inherit terminal background, got %v", x, y, cell.Style.Bg)
				}
				if (cell.Style.Attrs&uv.AttrReverse != 0) != wantCaret {
					t.Fatalf("focus %d: cell (%d, %d) inverse style does not match caret = %v", focus, x, y, wantCaret)
				}
			}
		}
	}
}

func first[T, U any](value T, _ U) T { return value }
