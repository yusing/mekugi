package activity

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

const reasoningSnapshotBody = "First I will inspect the stream renderer and its existing dialog.\n" +
	"This continues the same paragraph with **emphasis** and `inline code`.\n\n" +
	"- Keep the same bullet when its explanation wraps across the available width.\n" +
	"  This line continues that bullet with **more detail**.\n" +
	"- Preserve the final observation and reopen the complete reasoning after collapse."

func TestUISnapshotReasoningStreaming(t *testing.T) {
	for _, width := range []int{32, 80} {
		for _, titled := range []bool{false, true} {
			name := fmt.Sprintf("untitled_%d", width)
			body := reasoningSnapshotBody
			if titled {
				name = fmt.Sprintf("titled_%d", width)
				body = "**Inspecting the reasoning display**\n\n" + body
			}
			t.Run(name, func(t *testing.T) {
				p := Painter{Theme: livediff.DarkTheme}
				rows := p.Block(Block{Kind: "summary", Body: body, Live: true}, width)
				uisnapshot.Assert(t, "testdata/snapshots/reasoning_streaming_"+name+".txt", strings.Join(rows, "\n")+"\n")
			})
		}
	}
}

func TestUISnapshotReasoningCompleted(t *testing.T) {
	for _, width := range []int{32, 80} {
		for _, tc := range []struct {
			name      string
			collapsed bool
			hovered   bool
		}{
			{name: "linger"},
			{name: "collapsed", collapsed: true},
			{name: "hovered", collapsed: true, hovered: true},
		} {
			name := fmt.Sprintf("%s_%d", tc.name, width)
			t.Run(name, func(t *testing.T) {
				p := Painter{Theme: livediff.DarkTheme}
				block := Block{Kind: "summary", Body: reasoningSnapshotBody, Elapsed: "12s", Collapsed: tc.collapsed, Hovered: tc.hovered}
				uisnapshot.Assert(t, "testdata/snapshots/reasoning_completed_"+name+".txt", strings.Join(p.Block(block, width), "\n")+"\n")
			})
		}
	}
}

func TestUISnapshotReasoningDialog(t *testing.T) {
	for _, width := range []int{36, 84} {
		name := fmt.Sprintf("complete_%d", width)
		t.Run(name, func(t *testing.T) {
			p := Painter{Theme: livediff.DarkTheme}
			block := Block{Kind: "summary", Body: "**Inspecting the reasoning display**\n\n" + reasoningSnapshotBody, Elapsed: "12s", Collapsed: true}
			page := p.DialogPage(block, width-4)
			var rows []string
			for i := range page.Lines {
				rows = append(rows, page.Rows(i, width-4)...)
			}
			frame := DialogFrame{Page: page, Rows: rows, Total: len(rows), Footer: "esc close · ↑↓ scroll · c copy"}
			uisnapshot.Assert(t, "testdata/snapshots/reasoning_dialog_"+name+".txt", strings.Join(p.Dialog(frame, width, 28), "\n")+"\n")
		})
	}
}

func TestReasoningRowStyleResets(t *testing.T) {
	// Combined/short SGR forms emitted by wrapping and syntax rendering must
	// not turn a continuation back into normal text or strand its accent.
	row := reasoningRow("a\x1b[38;5;39mb\x1b[0;22;23;39mc\x1b[md")
	for _, fallback := range []bool{false, true} {
		screen := vt.NewEmulator(8, 1)
		frame := row
		if fallback {
			frame = FaintFallback(frame)
		}
		fmt.Fprint(screen, frame+"e")
		for x := range 4 {
			cell := screen.CellAt(x, 0)
			if cell.Style.Attrs&uv.AttrItalic == 0 {
				t.Fatalf("cell %d lost italic", x)
			}
			if !fallback && cell.Style.Attrs&uv.AttrFaint == 0 {
				t.Fatalf("cell %d lost faint", x)
			}
		}
		if !fallback && screen.CellAt(1, 0).Style.Fg != ansi.IndexedColor(39) {
			t.Fatal("reasoning overwrote inline color")
		}
		if !screen.CellAt(0, 0).Style.Equal(&screen.CellAt(2, 0).Style) || !screen.CellAt(0, 0).Style.Equal(&screen.CellAt(3, 0).Style) {
			t.Fatal("style reset stranded an inline color or changed body styling")
		}
		if cell := screen.CellAt(4, 0); !cell.Style.IsZero() {
			t.Fatalf("style leaked: %+v", cell.Style)
		}
		screen.Close()
	}
}
