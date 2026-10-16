package activity_test

import (
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
