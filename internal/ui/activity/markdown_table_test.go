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

func TestMarkdownTableAlignment(t *testing.T) {
	p := activityui.Painter{}
	got := strings.Join(plainLines(p.Markdown("Name | N | State\n:--- | ---: | :---:\n猫 | 12 | yes\nlong | 2 | no", 40)), "\n")
	want := "┌──────┬────┬───────┐\n│ Name │  N │ State │\n├──────┼────┼───────┤\n│ 猫   │ 12 │  yes  │\n│ long │  2 │  no   │\n└──────┴────┴───────┘"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMarkdownTableParsing(t *testing.T) {
	p := activityui.Painter{}
	for _, tc := range []struct {
		name, source string
		grid         bool
		content      string
	}{
		{"escaped", "| A | B |\n| --- | --- |\n| a\\|b | `x|y` |", true, "a|b"},
		{"triple code header", "```x|y``` | B\n--- | ---\nz | q", true, "x|y"},
		{"triple code body", "A | B\n--- | ---\n```x|y``` | z", true, "│ x|y │ z │"},
		{"double code", "A | B\n--- | ---\n``x|`y`` | z", true, "x|`y"},
		{"missing cells", "| A | B |\n| --- | --- |\n| x |", true, "│ x │   │"},
		{"header only", "| A | B |\n| --- | --- |", true, "│ A │ B │"},
		{"ordinary pipes", "| not a table |\nordinary text", false, "| not a table |"},
		{"incomplete delimiter", "| A | B |\n| --- | --", false, "| --- | --"},
		{"mismatched delimiter", "| A | B |\n| --- |", false, "| --- |"},
		{"bad colon", "A | B\n::--- | ---", false, "::--- | ---"},
		{"fenced", "```text\n| A | B |\n| --- | --- |\n```", false, "| --- | --- |"},
		{"quoted", "> | A | B |\n> | --- | --- |\n> | x | y |", true, "▎ ┌"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(plainLines(p.Markdown(tc.source, 50)), "\n")
			if strings.Contains(got, "┌") != tc.grid || !strings.Contains(got, tc.content) {
				t.Fatalf("unexpected render: %s", got)
			}
		})
	}
}

func TestMarkdownTableResponsive(t *testing.T) {
	p := activityui.Painter{}
	source := "Long heading | Value\n--- | ---\n**abcdefghijk** | 猫🙂\nsecond | z"
	for _, width := range []int{1, 2, 5, 12, 22, 40, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			rows := p.Markdown(source, width)
			var content strings.Builder
			for _, row := range rows {
				if ansi.StringWidth(row) > width {
					t.Fatalf("width %d exceeded: %q", width, row)
				}
				for _, r := range ansi.Strip(row) {
					if r >= 'a' && r <= 'z' {
						content.WriteRune(r)
					}
				}
			}
			if !strings.Contains(content.String(), "abcdefghijk") {
				t.Fatalf("lost wrapped cell: %q", rows)
			}
			if width >= 22 && !strings.Contains(ansi.Strip(rows[0]), "┌") {
				t.Fatalf("expected grid: %q", rows)
			}
			if width < 12 && strings.Contains(ansi.Strip(rows[0]), "┌") {
				t.Fatalf("expected records: %q", rows)
			}
		})
	}
}

func TestMarkdownTableLinkBoundaries(t *testing.T) {
	p := activityui.Painter{}
	for _, width := range []int{12, 22, 60} {
		rows := p.Markdown("Link | Flag\n--- | ---\n[abcdefghijk](https://example.com) | **YES**", width)
		screen := vt.NewEmulator(width+1, len(rows))
		defer screen.Close()
		letters := ""
		for y, row := range rows {
			_, _ = fmt.Fprintf(screen, "\x1b[%d;1H\x1b[0m%s", y+1, row)
			for x := range ansi.StringWidth(row) {
				cell := screen.CellAt(x, y)
				if strings.Contains("abcdefghijk", cell.Content) && cell.Content != "" && cell.Link.URL == "https://example.com" {
					letters += cell.Content
				}
				if cell.Content == "│" && cell.Link.URL != "" {
					t.Fatalf("linked border at %d,%d", x, y)
				}
				if cell.Content == "Y" && cell.Link.URL != "" {
					t.Fatal("link leaked into next cell")
				}
			}
		}
		if letters != "abcdefghijk" {
			t.Fatalf("width %d: wrapped link lost: %q", width, letters)
		}
	}
}

func TestMarkdownTableStopsAtBlocks(t *testing.T) {
	p := activityui.Painter{}
	for _, following := range []string{"# Notes | important | keep this", "- Notes | important | keep this", "* Notes | important | keep this", "> Notes | important | keep this"} {
		rows := plainLines(p.Markdown("A | B\n--- | ---\nx | y\n"+following, 60))
		if len(rows) != 6 || !strings.HasPrefix(rows[4], "└") || !strings.Contains(rows[5], "Notes | important | keep this") {
			t.Fatalf("block swallowed: %q", rows)
		}
	}
}

func TestMarkdownTableSummary(t *testing.T) {
	p := activityui.Painter{}
	for _, tc := range []struct{ source, want string }{
		{"A | B\n--- | ---\nx | y", "A: x · B: y"},
		{"> A | B\n> --- | ---\n> x | y", "A: x · B: y"},
		{"> > A | B\n> > --- | ---\n> > x | y", "A: x · B: y"},
		{"\n**A** | B\n--- | ---", "A · B"},
	} {
		got := p.Summary([]activityui.Block{{Kind: "final", Body: tc.source}})
		if got != tc.want {
			t.Fatalf("summary %q, want %q", got, tc.want)
		}
	}
}

func TestMarkdownTableWrappedHeaderStyle(t *testing.T) {
	p := activityui.Painter{}
	rows := p.Markdown("abcdefghijk | Value\n--- | ---\nx | y", 20)
	screen := vt.NewEmulator(21, len(rows))
	defer screen.Close()
	letters := ""
	for y, row := range rows {
		_, _ = fmt.Fprintf(screen, "\x1b[%d;1H\x1b[0m%s", y+1, row)
		if strings.HasPrefix(ansi.Strip(row), "├") {
			break
		}
		for x := range ansi.StringWidth(row) {
			cell := screen.CellAt(x, y)
			if cell.Content != "" && strings.Contains("abcdefghijk", cell.Content) && x < 10 {
				if cell.Style.Attrs&uv.AttrBold == 0 {
					t.Fatalf("unemphasized header at %d,%d: %#v", x, y, cell)
				}
				letters += cell.Content
			}
		}
	}
	if letters != "abcdefghijk" {
		t.Fatalf("header content lost: %q", letters)
	}
}

func TestMarkdownTableRecordAlignment(t *testing.T) {
	p := activityui.Painter{}
	source := "**A** | BBB | 猫\n--- | --- | ---\nabcdefghijklm | ok | yes\nz | fine | no"
	got := strings.Join(plainLines(p.Markdown(source, 16)), "\n")
	want := "A:   abcdefghijk\n     lm\nBBB: ok\n猫:  yes\n────────────────\nA:   z\nBBB: fine\n猫:  no"
	if got != want {
		t.Fatalf("record alignment:\n%s\nwant:\n%s", got, want)
	}
	// A single long label switches the entire record to stacked presentation.
	got = strings.Join(plainLines(p.Markdown("A | Long label\n--- | ---\nx | y", 11)), "\n")
	want = "A:\nx\nLong label:\ny"
	if got != want {
		t.Fatalf("stacked records: %q, want %q", got, want)
	}
}
