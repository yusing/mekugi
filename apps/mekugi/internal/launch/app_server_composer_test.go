package launch

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
	// The draft runs from the last prompt row, below the transcript, to the
	// dim rule or caption.
	rows := strings.Split(screen.String(), "\n")
	start := -1
	for y, row := range rows {
		if strings.HasPrefix(row, "❯ ") {
			start = y
		}
	}
	var text strings.Builder
	for y := start; start >= 0 && y < len(rows); y++ {
		if y > start && composerCaptionRow(screen, y, width, screen.CellAt(2, start).Style) {
			break
		}
		// Exclude the prompt column and right padding, preserving whitespace across wraps.
		text.WriteString(ansi.Cut(rows[y], 2, width-1))
	}
	return strings.TrimRight(text.String(), " ")
}

// composerStatusShows reports whether a screen's composer status row, above
// its prompt and any rule between them, starts with status.
func composerStatusShows(screen, status string) bool {
	rows := strings.Split(screen, "\n")
	for i := 0; i+1 < len(rows); i++ {
		next := i + 1
		if strings.HasPrefix(rows[next], "─") && next+1 < len(rows) {
			next++
		}
		if strings.HasPrefix(rows[i], status) && strings.HasPrefix(rows[next], "❯") {
			return true
		}
	}
	return false
}

// composerCaptionRow reports a row whose first visible cell leaves the plain
// draft style, as the dim composer caption does.
func composerCaptionRow(screen *vt.Emulator, y, width int, draft uv.Style) bool {
	for x := range width {
		if cell := screen.CellAt(x, y); cell != nil && strings.TrimSpace(cell.Content) != "" {
			return cell.Style != draft
		}
	}
	return false
}

func TestAppServerComposerFrame(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "first\n你好")
	frame, _ := u.testMainFrame(16, 8, 0)
	for i := range frame {
		frame[i] = ansi.Strip(frame[i])
	}
	// A blank status row, then the draft between rules; the caption is a
	// stat line below Main.
	want := []string{
		"",
		"────────────────",
		"❯ first",
		"  你好 ",
		"────────────────",
	}
	if len(frame) != 8 || !slices.Equal(frame[len(frame)-len(want):], want) {
		t.Fatalf("composer frame = %q, want final rows %q in 8-row frame", frame, want)
	}
	if u.Text != "first\n你好" {
		t.Fatalf("render mutated draft: %q", u.Text)
	}
}

func TestAppServerComposerResizeKeepsDraftTail(t *testing.T) {
	for _, size := range []struct {
		width, height, composerRows int
	}{{20, 8, 6}, {7, 6, 6}, {7, 3, 3}, {6, 2, 2}, {2, 1, 1}, {1, 1, 1}} {
		u, _ := newAppServerTestUI()
		u.Text = "old\nlines\nmore\n你好\nlast\nZ"
		frame, _ := u.testMainFrame(size.width, size.height, 0)
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
	appServerTestRequest(t, u, "1", "thread/start")
	appServerTestMessage(t, u, `{"id":1,"result":{"thread":{"id":"main"},"model":"actual-model","reasoningEffort":"high"}}`)
	bottom, _ := u.testStatLines(t, 70, 12)
	if bottom != "  actual-model (high)" {
		t.Fatalf("observed model caption: %q", bottom)
	}
	appServerTestRequest(t, u, "2", "thread/start")
	appServerTestMessage(t, u, `{"id":2,"result":{"thread":{"id":"new"},"reasoningEffort":null}}`)
	bottom, _ = u.testStatLines(t, 70, 12)
	if strings.TrimSpace(bottom) != "" {
		t.Fatalf("unknown model retained stale caption: %q", bottom)
	}
}

func TestAppServerComposerCaretFollowsFocusAndWrap(t *testing.T) {
	for _, focus := range []int{0, 1} {
		u, _ := newAppServerTestUI()
		u.shell.Focus = focus
		u.Text = "1234567890123" // Fills the 13-column input, placing its caret on the next row.
		screen := vt.NewEmulator(16, 8)
		t.Cleanup(func() { screen.Close() })
		if _, err := screen.Write([]byte(strings.Join(first(u.testMainFrame(16, 8, 0)), "\r\n"))); err != nil {
			t.Fatal(err)
		}
		for y := 4; y < 8; y++ {
			for x := range 16 {
				cell := screen.CellAt(x, y)
				if cell == nil {
					t.Fatalf("missing composer cell at (%d, %d)", x, y)
				}
				wantCaret := focus == 0 && x == 2 && y == 6
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
