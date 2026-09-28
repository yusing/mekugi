package activity_test

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestMutedAgentPalette(t *testing.T) {
	for _, tc := range []struct {
		name        string
		normal, dim int
	}{
		{"/root/agent6", 39, 31}, {"/root/agent1", 170, 133}, {"/root/agent5", 38, 30},
		{"/root/agent0", 99, 61}, {"/root/agent3", 37, 29}, {"/root/agent7", 133, 96}, {"/root/agent2", 74, 67},
	} {
		t.Run(tc.name, func(t *testing.T) {
			screen := vt.NewEmulator(4, 1)
			defer screen.Close()
			fmt.Fprint(screen, activityui.FaintFallback(activityui.Color(tc.name)+"N"+activityui.Reset+activityui.DimColor(tc.name)+"D"+activityui.Reset+"X"))
			expected := vt.NewEmulator(4, 1)
			defer expected.Close()
			fmt.Fprintf(expected, "\x1b[1;38;5;%dmN\x1b[0;38;5;%dmD\x1b[0mX", tc.normal, tc.dim)
			for x := range 3 {
				if !screen.CellAt(x, 0).Style.Equal(&expected.CellAt(x, 0).Style) {
					t.Fatalf("cell %d style = %+v, want %+v", x, screen.CellAt(x, 0).Style, expected.CellAt(x, 0).Style)
				}
			}
		})
	}
}

func TestWaitProgressMutedTargets(t *testing.T) {
	block := activityui.Block{Kind: "progress", Body: "Finished waiting", WaitTargets: []activityui.WaitTarget{
		{Name: "/root/agent1", Status: "Still running"}, {Name: "/root/agent6", Status: "completed"},
	}}
	source := "\x1b[38;5;243m• Finished waiting · \x1b[38;5;133magent1\x1b[38;5;243m: Still running, \x1b[38;5;31magent6\x1b[38;5;243m: completed\x1b[0m"
	if block.ProgressText() != ansi.Strip(strings.TrimPrefix(source, "\x1b[38;5;243m• ")) {
		t.Fatal("plain summary differs")
	}
	for _, theme := range []livediff.Theme{livediff.LightTheme, livediff.DarkTheme} {
		for _, width := range []int{18, 120} {
			p := activityui.Painter{Theme: theme}
			rows := p.Block(block, width)
			expected := vt.NewEmulator(120, 1)
			defer expected.Close()
			fmt.Fprint(expected, source)
			got := vt.NewEmulator(width+1, len(rows))
			defer got.Close()
			offset := 0
			for y, row := range rows {
				if ansi.StringWidth(row) > width {
					t.Fatalf("overflow: %q", row)
				}
				fmt.Fprintf(got, "\x1b[%d;1H\x1b[0m%s", y+1, activityui.FaintFallback(row))
				for x := range ansi.StringWidth(row) {
					cell := got.CellAt(x, y)
					if cell.Content == " " {
						continue
					}
					for expected.CellAt(offset, 0).Content == " " {
						offset++
					}
					want := expected.CellAt(offset, 0)
					if cell.Content != want.Content || !cell.Style.Equal(&want.Style) {
						t.Fatalf("width=%d cell %d,%d = %+v, want %+v", width, x, y, cell, want)
					}
					if cell.Style.Attrs&(uv.AttrFaint|uv.AttrBold) != 0 {
						t.Fatal("wait text uses faint or bold")
					}
					offset++
				}
			}
			if offset != ansi.StringWidth(source) {
				t.Fatal("missing progress text")
			}
		}
	}
}

func TestWaitProgressSanitizesHostText(t *testing.T) {
	b := activityui.Block{Kind: "progress", Body: "Waiting", WaitTargets: []activityui.WaitTarget{{Name: "/root/agent\x1b[31m", Status: "done\x1b[2J"}}}
	p := activityui.Painter{}
	rows := strings.Join(p.Block(b, 120), "\n")
	if strings.Contains(rows, "\x1b[31m") || strings.Contains(rows, "\x1b[2J") || ansi.Strip(rows) != "• Waiting · agent: done" {
		t.Fatalf("unsafe wait: %q", rows)
	}
}
