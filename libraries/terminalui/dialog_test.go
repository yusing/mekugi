package terminalui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotDialogSurfaceStyles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		theme diffrender.Theme
	}{
		{"terminal", diffrender.TerminalTheme},
		{"dark", diffrender.DarkTheme},
		{"light", diffrender.LightTheme},
	} {
		for _, fallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fallback=%v", tc.name, fallback), func(t *testing.T) {
				p := Painter{Theme: tc.theme}
				frame := DialogFrame{Page: DialogPage{Title: "Title", Detail: Dim + "metadata" + Undim}, Rows: []string{
					"A\x1b[0mB\x1b[mC\x1b[39;49mD",
					"\x1b[31mR\x1b[48;2;1;2;3mF\x1b[49mS",
				}, Footer: "esc close"}
				rows := p.Dialog(frame, 40, 8)
				if fallback {
					for i := range rows {
						rows[i] = FaintFallback(rows[i])
					}
				}
				rows = append(rows, "outside")
				path := fmt.Sprintf("testdata/snapshots/dialog_surface_%s_fallback_%t.txt", tc.name, fallback)
				uisnapshot.AssertTerminal(t, path, rows, 40)
			})
		}
	}
}

func TestDialogWrappedRowsMatchesWrap(t *testing.T) {
	for _, text := range []string{"", "abc def  ghi", "界界ab", "e\u0301👩‍💻text", "\x1b[31mcolored text\x1b[0m", "\x1b]8;;https://example.com\x1b\\linked text\x1b]8;;\x1b\\"} {
		for _, width := range []int{1, 2, 5, 20} {
			t.Run(fmt.Sprintf("%q/%d", text, width), func(t *testing.T) {
				if got, want := wrappedRows(text, width), len(Wrap(text, width, true)); got != want {
					t.Fatalf("rows=%d want %d", got, want)
				}
			})
		}
	}
	page := DialogPage{digits: 2, Lines: []DialogLine{{Number: 12, Gutter: "┆", Text: "abcdefgh"}}}
	rows := page.Rows(0, 9)
	if len(rows) != 2 || ansi.Strip(rows[0]) != "12 ┆ abcd" || ansi.Strip(rows[1]) != "   ┆ efgh" || page.Indent(0) != 5 || page.RowCount(0, 9) != 2 {
		t.Fatalf("wrapped numbered rows=%q", rows)
	}
}

func TestDialogFrameDimensions(t *testing.T) {
	var p Painter
	for _, width := range []int{12, 24, 80} {
		for _, height := range []int{5, 12} {
			frame := DialogFrame{Page: DialogPage{Title: strings.Repeat("title", 30), Detail: "detail", Live: true}, Position: "2 / 4", Paused: true, Rows: []string{strings.Repeat("界", 100), "short"}, Total: 100, Top: 50, Footer: "escape close · scroll"}
			rows := p.Dialog(frame, width, height)
			if len(rows) != height {
				t.Fatalf("height=%d want %d", len(rows), height)
			}
			for i, row := range rows {
				if got := ansi.StringWidth(row); got != width {
					t.Errorf("row %d width=%d want %d: %q", i, got, width, ansi.Strip(row))
				}
			}
		}
	}
	if p.Dialog(DialogFrame{}, 11, 10) != nil || p.Dialog(DialogFrame{}, 80, 4) != nil {
		t.Fatal("undersized frame rendered")
	}
}

func TestBackdropFadesColorsAndKeepsCursorMoves(t *testing.T) {
	row := "\x1b[1G\x1b[0m\x1b[38;2;1;2;3mSearch\x1b[0m\x1b[12G\x1b[48;2;22;42;29m+ added\x1b[0m"
	if got, want := Backdrop(row), Dim+"\x1b[1GSearch\x1b[12G+ added"; got != want {
		t.Fatalf("Backdrop = %q, want %q", got, want)
	}
}
