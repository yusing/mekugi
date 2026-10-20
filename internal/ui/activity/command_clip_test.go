package activity

import (
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestCommandClipPreservesWrappedRows(t *testing.T) {
	for _, code := range []string{
		`git status --short; git show --format=fuller HEAD && cat "a b.md" || true; echo $(a; b)`,
		`python3 -c "` + strings.Repeat(`print('long quoted source'); `, 80) + `"`,
		strings.Repeat("echo word | cat && ", 80) + "true",
		`printf '%s' '` + strings.Repeat("界e\u0301👩‍💻 ", 100) + `'`,
	} {
		for _, layout := range []bool{false, true} {
			p := Painter{Theme: livediff.DarkTheme, LayoutOnly: layout}
			block := Block{Kind: "op", Verb: "Run", Code: code, Lang: "bash", Fenced: true}
			for _, width := range []int{12, 44, 90} {
				full := p.Block(block, width)
				for _, budget := range []int{1, 2, 5, len(full), len(full) + 1} {
					for _, hover := range []bool{false, true} {
						block.SourceRows, block.Hovered = budget, hover
						want := clipSource(full, block, strings.Repeat(" ", block.cell(RowVerb(block))))
						if got := p.Block(block, width); !slices.Equal(got, want) {
							t.Fatalf("width=%d budget=%d layout=%v hover=%v: got %q, want %q", width, budget, layout, hover, got, want)
						}
					}
				}
				block.SourceRows = 0
			}
		}
	}
}

func TestShellWrapBoundsMaterializedRows(t *testing.T) {
	// Counting the complete geometry must not allocate or cut a styled row
	// for every hidden line of a large literal command.
	code := `python3 -c "` + strings.Repeat("x", 60_000) + `"`
	rows, hidden := shellWrap(code, code, 40, true, 5)
	if len(rows) != 5 || hidden < 1500 {
		t.Fatalf("materialized %d rows, hid %d", len(rows), hidden)
	}
}

func BenchmarkCommandClippedLayout(b *testing.B) {
	code := `python3 -c "` + strings.Repeat("x", 60_000) + `"`
	for _, budget := range []int{0, 5} {
		name := "full"
		if budget != 0 {
			name = "clipped"
		}
		b.Run(name, func(b *testing.B) {
			p := Painter{Theme: livediff.DarkTheme, LayoutOnly: true}
			block := Block{Kind: "op", Verb: "Run", Code: code, Lang: "bash", Fenced: true, SourceRows: budget}
			b.ReportAllocs()
			for b.Loop() {
				p.Block(block, 90)
			}
		})
	}
}
