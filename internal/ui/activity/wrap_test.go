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

func TestUnderlinePreservesStyledRows(t *testing.T) {
	p := activityui.Painter{}
	for name, source := range map[string]string{
		"edit":   p.Label("Edit", "`internal/ui/activity/paint.go` +4 -2 · python3, gofmt"),
		"resets": "\x1b[32mEdited\x1b[0m path\x1b[m +4\x1b[24;39m -2",
		"link":   "\x1b]8;;https://example.com\x1b\\file\x1b]8;;\x1b\\\x1b[24;39m tail",
	} {
		t.Run(name, func(t *testing.T) {
			width := ansi.StringWidth(source)
			want := vt.NewEmulator(width+2, 1)
			defer want.Close()
			got := vt.NewEmulator(width+2, 1)
			defer got.Close()
			_, _ = want.Write([]byte(source + "!"))
			_, _ = got.Write([]byte(activityui.Underline(source) + "!"))
			for x := range width + 1 {
				expected := *want.CellAt(x, 0)
				if x < width {
					expected.Style.Underline = uv.UnderlineSingle
				}
				cell := got.CellAt(x, 0)
				if cell.Content != expected.Content || !cell.Style.Equal(&expected.Style) || cell.Link != expected.Link {
					t.Fatalf("cell %d = %#v, want %#v", x, cell, expected)
				}
			}
		})
	}
}

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

func TestReadsTakeOneRowEachWhenWrapped(t *testing.T) {
	p := activityui.Painter{}
	block := activityui.Block{Kind: "reads", Verb: "Read", Reads: []activityui.Read{
		{Path: "internal/router/live_activity_view.go", Ranges: []string{"1:100"}},
		{Path: "internal/router/app_server_ui.go"},
		{Path: "internal/router/live_activity_conversation.go"},
	}}
	rows := p.Block(block, 60)
	if len(rows) != 3 {
		t.Fatalf("rows = %q", rows)
	}
	for i, row := range rows {
		plain := ansi.Strip(row)
		if strings.Contains(plain, "·") || !strings.HasSuffix(plain, ".go") && !strings.HasSuffix(plain, "L1–100") || ansi.StringWidth(row) > 60 {
			t.Fatalf("row %d = %q", i, plain)
		}
	}
	if fits := p.Block(block, 200); len(fits) != 1 || strings.Count(ansi.Strip(fits[0]), "·") != 2 {
		t.Fatalf("fitting reads = %q", fits)
	}
}

func TestWrappedLinksDoNotStylePaddingOrGutters(t *testing.T) {
	p := activityui.Painter{}
	for _, hard := range []bool{false, true} {
		rows := activityui.Wrap(p.Inline("see [timestamp assignment](https://example.com) end"), 15, hard)
		screen := vt.NewEmulator(20, len(rows))
		defer screen.Close()
		var linked strings.Builder
		for y, row := range rows {
			_, _ = fmt.Fprintf(screen, "\x1b[%d;1H| %s%s", y+1, row, strings.Repeat(" ", 18-ansi.StringWidth(row)))
			for x := range 20 {
				cell := screen.CellAt(x, y)
				if x < 2 || x >= 2+ansi.StringWidth(row) {
					if cell.Style.Underline != uv.UnderlineNone || cell.Link.URL != "" {
						t.Fatalf("styled gutter/padding at %d,%d: %#v", x, y, cell)
					}
				}
				if cell.Link.URL != "" {
					if cell.Style.Underline != uv.UnderlineSingle {
						t.Fatalf("lost underline: %#v", cell)
					}
					linked.WriteString(cell.Content)
				}
			}
		}
		if strings.ReplaceAll(linked.String(), " ", "") != "timestampassignment" {
			t.Fatalf("lost link content: %q", linked.String())
		}
	}
}
