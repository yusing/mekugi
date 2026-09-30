package activity_test

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestThinkingBlock(t *testing.T) {
	var p activityui.Painter
	body := "one\n\ntwo\n\nthree\n\nfour\n\nfive"
	plain := func(block activityui.Block) []string {
		rows := p.Block(block, 40)
		for i, row := range rows {
			rows[i] = strings.TrimRight(ansi.Strip(row), " ")
		}
		return rows
	}
	// Streaming provider reasoning keeps its latest text rows under a header.
	if got := plain(activityui.Block{Kind: "summary", Body: body, Live: true}); strings.Join(got, "|") != "• Thinking… · +2 lines|  three|  four|  five" {
		t.Fatalf("live thinking = %q", got)
	}
	if got := plain(activityui.Block{Kind: "summary", Body: "one\n\ntwo", Live: true}); strings.Join(got, "|") != "• Thinking…|  one|  two" {
		t.Fatalf("short live thinking = %q", got)
	}
	// Finished thinking reports its time and keeps every row.
	got := plain(activityui.Block{Kind: "summary", Body: body, Elapsed: "12s"})
	if got[0] != "• Thought for 12s" || len(got) != 10 || got[9] != "  five" {
		t.Fatalf("finished thinking = %q", got)
	}
	if got := plain(activityui.Block{Kind: "summary", Body: body, Elapsed: "12s", Collapsed: true}); strings.Join(got, "|") != "• Thought for 12s" {
		t.Fatalf("folded thinking = %q", got)
	}
	if got := plain(activityui.Block{Kind: "summary", Body: "one"}); strings.Join(got, "|") != "• Thought|  one" {
		t.Fatalf("untimed thinking = %q", got)
	}
	// Titled and untitled summaries share the streaming lifecycle.
	if got := plain(activityui.Block{Kind: "summary", Body: "**Checking**\n\nPublic summary.", Live: true}); strings.Join(got, "|") != "• Thinking…|  Public summary." {
		t.Fatalf("titled summary = %q", got)
	}
	if got := plain(activityui.Block{Kind: "summary", Body: "**Check", Live: true}); !strings.Contains(strings.Join(got, "|"), "Thinking…") {
		t.Fatalf("partial title lost thinking header = %q", got)
	}
}

func TestReasoningMultilineStyles(t *testing.T) {
	var p activityui.Painter
	body := "**Checking**\n\nFirst **bold** then normal and `code` then normal.\nSoft continuation with [link](https://example.com) then normal.\n\n- One long explicit list item that wraps across lines\n  still the same item.\n\nLast paragraph."
	for _, width := range []int{24, 80} {
		rows := p.Block(activityui.Block{Kind: "summary", Body: body}, width)
		screen := vt.NewEmulator(width, len(rows)+1)
		fmt.Fprint(screen, strings.Join(rows, "\r\n")+"\r\n"+"Answer")
		for y := 1; y < len(rows); y++ {
			for x := range width {
				cell := screen.CellAt(x, y)
				if cell == nil || strings.TrimSpace(cell.Content) == "" {
					continue
				}
				if cell.Style.Attrs&(uv.AttrFaint|uv.AttrItalic) != uv.AttrFaint|uv.AttrItalic {
					t.Errorf("width %d cell (%d,%d) %q lost reasoning style: %+v", width, x, y, cell.Content, cell.Style)
				}
			}
		}
		if cell := screen.CellAt(0, len(rows)); cell.Style.Attrs&(uv.AttrFaint|uv.AttrItalic) != 0 {
			t.Error("reasoning style leaked to the answer")
		}
		screen.Close()
		plain := ansi.Strip(strings.Join(rows, "\n"))
		if strings.Count(plain, "•") != 2 {
			t.Fatalf("paragraphs introduced extra bullets: %s", plain)
		}
		if strings.Contains(plain, "**") || strings.Contains(plain, "`") {
			t.Fatal("inline Markdown not rendered")
		}
	}
}
