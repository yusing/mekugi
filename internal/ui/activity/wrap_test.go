package activity_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestLiveActivityWrapPreservesTerminalStyles(t *testing.T) {
	p := activityui.Painter{}
	for name, text := range map[string]string{
		"receipt":          p.Label("Create", "`internal/router/live_activity_quote_test.go` +46 -0 · cat, python3"),
		"combined":         "\x1b[38;5;114m\x1b[2mone two three four\x1b[22mfive six\x1b[39mseven",
		"partial resets":   "\x1b[1;3;4;38;2;40;80;120mone two\x1b[23mthree four\x1b[24mfive six\x1b[0mseven eight",
		"explicit newline": "\x1b[31m\x1b[1mone two\nthree four\x1b[mfive six",
		"hyperlink":        "\x1b[35m\x1b[4m\x1b]8;;https://example.com\x1b\\one two three four\x1b]8;;\x1b\\\x1b[24;39mfive",
	} {
		for _, hard := range []bool{false, true} {
			for _, width := range []int{1, 8, 24, 60} {
				t.Run(fmt.Sprintf("%s/hard=%t/width=%d", name, hard, width), func(t *testing.T) {
					// Compare actual terminal cell styles against the unwrapped source.
					// Reset between rows as independent viewport paints and gutters do.
					source := strings.ReplaceAll(text, "\n", "")
					want := vt.NewEmulator(ansi.StringWidth(source)+1, 1)
					defer want.Close()
					_, _ = want.Write([]byte(source))
					rows := activityui.Wrap(text, width, hard)
					got := vt.NewEmulator(width+1, len(rows))
					defer got.Close()
					x := 0
					for y, row := range rows {
						_, _ = fmt.Fprintf(got, "\x1b[%d;1H\x1b[0m%s\x1b[0m", y+1, row)
						for col := range ansi.StringWidth(row) {
							cell := got.CellAt(col, y)
							if cell.Content == " " {
								continue
							}
							for want.CellAt(x, 0).Content == " " {
								x++
							}
							expected := want.CellAt(x, 0)
							if cell.Content != expected.Content || !cell.Style.Equal(&expected.Style) {
								t.Fatalf("cell (%d,%d) = %#v, want %#v", col, y, cell, expected)
							}
							x++
						}
					}
					if x != ansi.StringWidth(source) {
						t.Fatalf("rendered %d source cells, want %d", x, ansi.StringWidth(source))
					}
				})
			}
		}
	}
}
