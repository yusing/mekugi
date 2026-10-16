package livediff

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func testThemeChunk(key, path, diff string) Chunk {
	return Chunk{
		Key:    key,
		Review: diffmodel.ReviewFile{BeforePath: path, AfterPath: path, Diff: diff},
	}
}

func assertNoBackground(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, "\x1b[48;") {
		t.Fatalf("unexpected background color: %q", text)
	}
}

func TestLiveDiffThemeGeometryAndFallback(t *testing.T) {
	chunk := testThemeChunk("edit", "file.go", "@@ -1 +1 @@\n-return \"界\"\n+return len(\"界\")\n")
	chunk.Highlighted = true
	file := File{Path: "file.go", Highlighted: true, Chunks: []Chunk{chunk}}
	for _, width := range []int{1, 13, 90} {
		var baseline string
		for _, theme := range []diffrender.Theme{diffrender.TerminalTheme, diffrender.LightTheme, diffrender.DarkTheme} {
			render, err := new(Renderer).Render(t.Context(), theme, []File{file}, "", width)
			if err != nil {
				t.Fatal(err)
			}
			output := strings.Join(render.Lines, "\n")
			if theme == diffrender.TerminalTheme {
				baseline = ansi.Strip(output)
			} else if ansi.Strip(output) != baseline {
				t.Fatalf("theme %d changed geometry at width %d: %q", theme, width, output)
			}
			for _, line := range render.Lines {
				if ansi.StringWidth(line) > width-1 || textsanitize.Safe(line, true) != line {
					t.Fatalf("theme %d produced unsafe or overflowing output: %q", theme, line)
				}
			}
		}
	}
	for _, theme := range []diffrender.Theme{diffrender.LightTheme, diffrender.DarkTheme} {
		for _, path := range []string{"file.go", "unknown.extension"} {
			for _, source := range []string{"\"unfinished\n", "/* comment\n", "\n\n", strings.Repeat("x", diffrender.MaxSyntaxBytes+1) + "\n"} {
				lines, err := new(diffrender.Renderer).ColorSource(t.Context(), theme, path, source)
				if err != nil || ansi.Strip(strings.Join(lines, "\n"))+"\n" != source {
					t.Fatalf("theme %d changed incomplete/unsupported source for %s: %v", theme, path, err)
				}
			}
		}
	}
}

func TestUISnapshotLiveDiffRowFills(t *testing.T) {
	chunk := testThemeChunk("edit", "file.go",
		"@@ -9,3 +19,3 @@\n context\n-return \"old\"\n+return \"界\"\n tail\n")
	chunk.Status = ""
	for _, theme := range []diffrender.Theme{diffrender.TerminalTheme, diffrender.LightTheme, diffrender.DarkTheme} {
		for _, width := range []int{1, 2, 3, 8, 80} {
			render, err := new(Renderer).Render(t.Context(), theme, []File{{Path: "file.go", Chunks: []Chunk{chunk}}}, "", width)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range render.Lines {
				if ansi.StringWidth(line) > width-1 {
					t.Fatalf("overflow: %q", line)
				}
				logical, exact := slices.BinarySearch(render.RowStarts, i)
				if !exact {
					logical--
				}
				sourceIndex := logical - (len(render.RowStarts) - 4)
				if sourceIndex != 1 && sourceIndex != 2 {
					assertNoBackground(t, line)
					continue
				}
				if width > 3 {
					if ansi.StringWidth(line) != width-1 ||
						!strings.HasSuffix(line, "\x1b[0m") {
						t.Fatalf("missing full-width bounded fill: %q", line)
					}
					if strings.Contains(line, "\x1b[39m") {
						t.Fatalf("token reset escaped row Foreground: %q", line)
					}
				}
			}
			if width == 80 {
				for i, prefix := range []string{"  19│ context", "  10│-return", "  20│+return", "  21│ tail"} {
					if !strings.HasPrefix(ansi.Strip(render.Lines[i+1]), prefix) {
						t.Fatalf("wrong single-column coordinate: %q", render.Lines[i+1])
					}
				}
			}
			if width == 8 || width == 80 {
				rows := append(slices.Clone(render.Lines), "plain")
				uisnapshot.AssertTerminal(t, "testdata/snapshots/row_fills_"+strconv.Itoa(int(theme))+"_"+strconv.Itoa(width)+".txt", rows, width)
			}
		}
	}
}
