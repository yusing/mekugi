package livediff

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
)

func viewportSyntaxFiles(count int, extension string) []File {
	files := make([]File, count)
	for i := range files {
		path := fmt.Sprintf("viewport-%d.%s", i, extension)
		diff := fmt.Sprintf("@@ -1,5 +1,5 @@\n /* file %d multiline\n  comment continues %s\n  */\n-old := `old value`\n+next := `new value`\n tail\n@@ -20,2 +20,2 @@\n-oldSecond%d := 1\n+newSecond%d := 2\n end\n\\ No newline at end of file\n", i, strings.Repeat("界 é ", 18), i, i)
		chunk := testThemeChunk(fmt.Sprintf("edit-%d", i), path, diff)
		chunk.Highlighted = i == 1
		chunk.Change = fmt.Sprintf("change-%d", i)
		files[i] = File{Path: path, Highlighted: chunk.Highlighted, Chunks: []Chunk{chunk}}
	}
	return files
}

// Compare observable geometry separately from private lazy-paint bookkeeping.
func assertViewportGeometry(t *testing.T, got, want Render) {
	t.Helper()
	if !reflect.DeepEqual(got.Starts, want.Starts) ||
		!reflect.DeepEqual(got.Hunks, want.Hunks) ||
		!reflect.DeepEqual(got.RowStarts, want.RowStarts) ||
		!reflect.DeepEqual(got.Sources, want.Sources) ||
		!reflect.DeepEqual(got.Counts, want.Counts) ||
		got.FocusOffset != want.FocusOffset || got.FocusRow != want.FocusRow ||
		len(got.Lines) != len(want.Lines) {
		t.Fatal("viewport painting changed geometry, navigation, attribution, counts, or focus")
	}
	for i := range got.Lines {
		if ansi.Strip(got.Lines[i]) != ansi.Strip(want.Lines[i]) {
			t.Fatalf("row %d source/layout differs: got %q, want %q", i, got.Lines[i], want.Lines[i])
		}
	}
}

func TestLiveDiffViewportSyntaxMatchesEager(t *testing.T) {
	files := viewportSyntaxFiles(3, "go")
	focus := files[1].Chunks[0]
	var renderer Renderer
	for _, theme := range []Theme{DarkTheme, LightTheme, TerminalTheme} {
		for _, width := range []int{80, 22, 7, 1, 80} {
			t.Run(fmt.Sprintf("theme-%d/width-%d", theme, width), func(t *testing.T) {
				want, err := new(Renderer).Render(t.Context(), theme, files, "", width, 1, focus)
				if err != nil {
					t.Fatal(err)
				}
				if width == 80 && !strings.Contains(ansi.Strip(strings.Join(want.Lines, "\n")), "\\ No newline at end of file") {
					t.Fatal("fixture lost its no-newline marker")
				}
				renderer.LayoutOnly = true
				got, err := renderer.Render(t.Context(), theme, files, "", width, 1, focus)
				if err != nil {
					t.Fatal(err)
				}
				renderer.LayoutOnly = false
				assertViewportGeometry(t, got, want)
				if len(got.Hunks) != 6 {
					t.Fatalf("fixture has %d hunks, want 6", len(got.Hunks))
				}
				// Enter mid-comment, then scroll across files and return to warm spans.
				for _, hunk := range []int{2, 5, 0, 3, 2, 1, 4, 5} {
					start := got.Hunks[hunk]
					end := len(got.Lines)
					if hunk+1 < len(got.Hunks) {
						end = got.Hunks[hunk+1]
					}
					visible := min(start+2, end-1)
					if err := got.PaintViewport(t.Context(), visible, visible+1); err != nil {
						t.Fatal(err)
					}
					assertViewportGeometry(t, got, want)
					// A one-row viewport paints the entire intersecting hunk, so
					// multiline lexer state and wrapped continuations match eager.
					for row := start; row < end; row++ {
						if got.Lines[row] != want.Lines[row] {
							t.Fatalf("hunk %d row %d ANSI differs: got %q, want %q", hunk, row, got.Lines[row], want.Lines[row])
						}
					}
				}
				if !slices.Equal(got.Lines, want.Lines) {
					t.Fatal("fully visited render differs from eager ANSI")
				}
			})
		}
	}
}

type viewportCountingLexer struct {
	chroma.Lexer
	sources []string
}

func (l *viewportCountingLexer) Tokenise(options *chroma.TokeniseOptions, text string) (chroma.Iterator, error) {
	l.sources = append(l.sources, text)
	return l.Lexer.Tokenise(options, text)
}

func TestLiveDiffViewportSyntaxOnlyTokenizesIntersectingHunks(t *testing.T) {
	original := lexers.GlobalLexerRegistry
	lexers.GlobalLexerRegistry = chroma.NewLexerRegistry()
	t.Cleanup(func() { lexers.GlobalLexerRegistry = original })
	lexer := &viewportCountingLexer{Lexer: chroma.MustNewLexer(&chroma.Config{
		Name: "viewport-fixture", Filenames: []string{"*.viewportfixture"},
	}, func() chroma.Rules {
		return chroma.Rules{"root": {{Pattern: `(?s).+`, Type: chroma.Keyword}}}
	})}
	lexers.Register(lexer)
	files := viewportSyntaxFiles(4, "viewportfixture")
	renderer := Renderer{LayoutOnly: true}
	render, err := renderer.Render(t.Context(), DarkTheme, files, "", 22, 1, files[1].Chunks[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(lexer.sources) != 0 {
		t.Fatalf("layout tokenized %d sources", len(lexer.sources))
	}
	renderer.LayoutOnly = false
	before := slices.Clone(render.Lines)
	start, end := render.Hunks[2], render.Hunks[3]
	if err := render.PaintViewport(t.Context(), start+2, start+3); err != nil {
		t.Fatal(err)
	}
	if len(lexer.sources) != 2 {
		t.Fatalf("one changed hunk tokenized %d sources, want both sides only", len(lexer.sources))
	}
	for _, source := range lexer.sources {
		if !strings.Contains(source, "file 1 multiline") || strings.Contains(source, "Second") {
			t.Fatalf("paint tokenized a hidden hunk or lost full-hunk input: %q", source)
		}
	}
	changed := false
	for i := range render.Lines {
		if i >= start && i < end {
			changed = changed || render.Lines[i] != before[i]
		} else if render.Lines[i] != before[i] {
			t.Fatalf("offscreen hunk row %d was painted", i)
		}
	}
	if !changed {
		t.Fatal("visible syntax was not decorated")
	}
	painted := slices.Clone(render.Lines)
	for _, viewport := range [][2]int{{start + 2, start + 3}, {start, end}, {start + 1, start + 1}, {end, end}} {
		if err := render.PaintViewport(t.Context(), viewport[0], viewport[1]); err != nil {
			t.Fatal(err)
		}
	}
	if len(lexer.sources) != 2 || !slices.Equal(render.Lines, painted) {
		t.Fatal("warm or empty viewport retokenized sources or changed ANSI")
	}
	// The half-open end must not admit the neighboring hunk. Scrolling
	// into that hunk now tokenizes its two complete sides, not other files.
	if err := render.PaintViewport(t.Context(), end, end+1); err != nil {
		t.Fatal(err)
	}
	if len(lexer.sources) != 4 {
		t.Fatalf("scroll tokenized %d sources, want 4 total", len(lexer.sources))
	}
	for _, source := range lexer.sources[2:] {
		if !strings.Contains(source, "Second1") || strings.Contains(source, "multiline") {
			t.Fatalf("scroll tokenized wrong hunk: %q", source)
		}
	}
}

func BenchmarkLiveDiffColdViewportSyntax(b *testing.B) {
	files := viewportSyntaxFiles(120, "go")
	for _, layoutOnly := range []bool{false, true} {
		name := "eager"
		if layoutOnly {
			name = "layout-plus-viewport"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				renderer := Renderer{LayoutOnly: layoutOnly}
				render, err := renderer.Render(b.Context(), DarkTheme, files, "", 80, 0, files[0].Chunks[0])
				if err != nil {
					b.Fatal(err)
				}
				if layoutOnly {
					renderer.LayoutOnly = false
					if err := render.PaintViewport(b.Context(), 0, min(24, len(render.Lines))); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
