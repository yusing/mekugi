package terminalview

import (
	"testing"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestScrollableDiffKeys(t *testing.T) {
	for _, test := range []struct {
		name, key        string
		offset, starting int
	}{
		{"line down", "j", 51, 50},
		{"line up", "k", 49, 50},
		{"down arrow", "\x1b[B", 51, 50},
		{"up arrow", "\x1b[A", 49, 50},
		{"page down", "\x1b[6~", 60, 50},
		{"space page down", " ", 60, 50},
		{"page up", "\x1b[5~", 40, 50},
		{"b page up", "b", 40, 50},
		{"home", "\x1b[H", 0, 50},
		{"g home", "g", 0, 50},
		// The pinned heading leaves nine source rows in the ten-row pane.
		{"end", "\x1b[F", 91, 50},
		{"G end", "G", 91, 50},
		{"removed saved resume shortcut", "r", 50, 50},
		{"clamp at top", "k", 0, 0},
		{"clamp at bottom", "j", 99, 99},
		{"clamp page at bottom", " ", 99, 95},
	} {
		t.Run(test.name, func(t *testing.T) {
			pane := newTerminalScrollTestPane(test.starting)
			pane.Key(test.key)
			if got := pane.view.Scroll["scroll.txt"]; got != test.offset {
				t.Fatalf("diff offset = %d, want %d", got, test.offset)
			}
		})
	}
}

func newTerminalScrollTestPane(offset int) *Pane {
	pane := NewPane("", diffrender.DarkTheme)
	lines := make([]string, 100)
	pane.lines, pane.offset, pane.rows = lines, offset, 10
	pane.lastWidth, pane.lastHeight = 80, 20
	pane.rendering = Render{Layout: diffrender.Layout{Lines: lines}, Starts: []int{0}}
	pane.view = View{
		View:   presentation.View{Files: []presentation.File{{Path: "scroll.txt"}}},
		Scroll: map[string]int{"scroll.txt": offset},
	}
	return pane
}

func TestScrollableDiffBeforePaintAndAfterFileChange(t *testing.T) {
	for _, geometry := range []struct {
		name   string
		render Render
	}{
		{name: "not painted"},
		{name: "removed file", render: Render{Layout: diffrender.Layout{Lines: []string{"old", "other"}}, Starts: []int{0, 1}}},
	} {
		t.Run(geometry.name, func(t *testing.T) {
			for _, key := range []string{"\x1b[5~", "\x1b[6~", "\x1b[A", "\x1b[B", "g", "G", "wheel up"} {
				pane := newTerminalScrollTestPane(50)
				pane.rendering = geometry.render
				pane.lines = geometry.render.Lines
				if key == "wheel up" {
					pane.Mouse('k', 2, 1)
				} else {
					pane.Key(key)
				}
				if pane.view.Selected != 0 || pane.view.Scroll["scroll.txt"] != 50 {
					t.Fatalf("%q used stale geometry: selection=%d scroll=%v", key, pane.view.Selected, pane.view.Scroll)
				}
				// Valid painted geometry restores ordinary scrolling immediately.
				pane.rendering = Render{Layout: diffrender.Layout{Lines: make([]string, 100)}, Starts: []int{0}}
				pane.lines, pane.offset = pane.rendering.Lines, 50
				pane.Key("k")
				if pane.view.Scroll["scroll.txt"] != 49 {
					t.Fatalf("scroll did not recover after paint: %v", pane.view.Scroll)
				}
			}
		})
	}
}
