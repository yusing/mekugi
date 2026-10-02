package activity

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestSearchOutputSelectedTailParity(t *testing.T) {
	for _, tc := range []struct {
		name, label string
		rows        []string
	}{
		{"paths", "", []string{"old.go:1:var old = 0", "", "main.go:2:var answer = 42", "", "main.py:3:return 42"}},
		{"unknown paths", "", []string{"old.unknown:1:old", "", "result.unknown:2:plain output", "unstructured output"}},
		{"single file", "Search in `main.go`", []string{"1:var old = 0", "", "2:var answer = 42", "", "3:println(answer)"}},
		{"invalid label", "Search in main.go", []string{"1:old", "", "2:var answer = 42", "3:println(answer)"}},
		{"long line", "", []string{"old.go:1:var old = 0", "", "main.go:2:var text = \"" + strings.Repeat("x", OutputTailBytes+100) + "\"", "main.go:3:println(text)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Painter{}
			for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
				p.Theme = theme
				for _, suffix := range []string{"", " // changed"} {
					rows := slices.Clone(tc.rows)
					rows[len(rows)-1] += suffix
					block := selectedOutputBlock(Block{Verb: "Search", Label: tc.label}, rows, 2)
					full := new(Painter)
					full.Theme = theme
					colors := full.outputColors(block, block.Output.View().Lines)
					var want []string
					for i := block.TailOmitted; i < len(rows); i++ {
						text := outputText([]byte(rows[i]))
						if text == "" {
							continue
						}
						colored := colors[i]
						if rows[i] != text {
							colored = ansi.Truncate(colored, ansi.StringWidth(text), "…")
						}
						want = append(want, colored)
					}
					if got := p.tailColors(block); !slices.Equal(got, want) {
						t.Fatalf("theme=%v changed=%v: got %q, want %q", theme, suffix != "", got, want)
					}
				}
			}
		})
	}
}

func TestOutputSelectedTailPreservesSourceAndDiffContext(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block Block
		rows  []string
		start int
	}{
		{"multiline source", Block{Verb: "Read", Kind: "reads", Reads: []Read{{Path: "main.go"}}}, []string{"package main", "/* comment starts", "var answer = 42", "comment ends */"}, 2},
		{"search diff precedence", Block{Verb: "Search"}, []string{"--- a/main.go", "+++ b/main.go", "@@ -1 +1 @@", "-var answer = 41", "+var answer = 42"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
				p := Painter{Theme: theme}
				block := selectedOutputBlock(tc.block, tc.rows, tc.start)
				full := p.outputColors(block, tc.rows)
				want := full[tc.start:]
				if got := p.tailColors(block); !slices.Equal(got, want) {
					t.Fatalf("lost context: got %q, want %q", got, want)
				}
				isolated := new(Painter)
				isolated.Theme = theme
				if slices.Equal(isolated.outputColors(block, block.Tail), want) {
					t.Fatal("fixture does not distinguish retained context from isolated tail")
				}
			}
		})
	}
}

func TestSearchOutputSelectedTailRetainsHighlightLimit(t *testing.T) {
	rows := make([]string, 600)
	for i := range rows {
		rows[i] = fmt.Sprintf("main.go:%d:var text = \"%s\"", i+1, strings.Repeat("x", 450))
	}
	if len(strings.Join(rows, "\n")) <= dialogHighlightBytes {
		t.Fatal("fixture must exceed highlighting limit")
	}
	p := Painter{Theme: livediff.DarkTheme}
	block := selectedOutputBlock(Block{Verb: "Search"}, rows, len(rows)-2)
	if got := p.tailColors(block); !slices.Equal(got, block.Tail) {
		t.Fatalf("small selected tail bypassed retained-content limit: %q", got)
	}
	if got := p.outputColors(block, block.Output.View().Lines); !slices.Equal(got, rows) {
		t.Fatal("dialog bypassed retained-content limit")
	}
}

type searchTailCountingLexer struct {
	chroma.Lexer
	sources []string
}

func (l *searchTailCountingLexer) Tokenise(options *chroma.TokeniseOptions, text string) (chroma.Iterator, error) {
	l.sources = append(l.sources, text)
	return l.Lexer.Tokenise(options, text)
}

func TestSearchOutputSelectedTailSkipsOffscreenTokenization(t *testing.T) {
	// This scoped registry fixture is deliberately not parallel: only the
	// selected rows should reach a lexer, and repeated paints must stay cached.
	original := lexers.GlobalLexerRegistry
	lexers.GlobalLexerRegistry = chroma.NewLexerRegistry()
	t.Cleanup(func() { lexers.GlobalLexerRegistry = original })
	lexer := &searchTailCountingLexer{Lexer: chroma.MustNewLexer(&chroma.Config{
		Name: "search-tail-fixture", Filenames: []string{"*.fixture"},
	}, func() chroma.Rules {
		return chroma.Rules{"root": {{Pattern: `(?s).+`, Type: chroma.Keyword}}}
	})}
	lexers.Register(lexer)
	rows := make([]string, 200)
	for i := range rows {
		rows[i] = fmt.Sprintf("result.fixture:%d:value%d", i+1, i)
	}
	block := selectedOutputBlock(Block{Verb: "Search"}, rows, len(rows)-3)
	p := Painter{Theme: livediff.DarkTheme}
	first := p.tailColors(block)
	wantSources := []string{"value197\n", "value198\n", "value199\n"}
	if !slices.Equal(lexer.sources, wantSources) {
		t.Fatalf("offscreen rows tokenized: got %q, want %q", lexer.sources, wantSources)
	}
	for range 4 {
		if got := p.tailColors(block); !slices.Equal(got, first) {
			t.Fatal("repeated selected tail changed ANSI output")
		}
	}
	if !slices.Equal(lexer.sources, wantSources) {
		t.Fatalf("offscreen cache churn retokenized selected tail: %q", lexer.sources)
	}
	// Dialogs still color every retained row; selection must not become a
	// permanent truncation of the stored output.
	if got := p.outputColors(block, rows); len(got) != len(rows) {
		t.Fatalf("dialog lost rows: %d", len(got))
	}
	if len(lexer.sources) <= 128 {
		t.Fatalf("dialog fixture did not exercise cache capacity: %d", len(lexer.sources))
	}
}

func selectedOutputBlock(block Block, rows []string, start int) Block {
	block.Output = new(Output)
	block.Output.Write(strings.Join(rows, "\n"))
	block.TailOmitted = start
	for _, row := range rows[start:] {
		if text := outputText([]byte(row)); text != "" {
			block.Tail = append(block.Tail, text)
		}
	}
	return block
}

func TestUISnapshotSearchOutputSelectedTail(t *testing.T) {
	rows := []string{
		"internal/router/old.go:12:var earlier = 0",
		"internal/router/old.go:18:var hidden = 1",
		"",
		"internal/router/main.go:30:func answer() int {",
		"internal/router/main.go:31:    return 42",
		"",
		"internal/router/main.go:32:}",
		"internal/ui/activity/output.go:40:var ready = true",
		"internal/ui/activity/output.go:41:println(ready)",
	}
	block := selectedOutputBlock(Block{Kind: "op", Verb: "Search", Label: "`answer` in `internal`", Running: true}, rows, 3)
	p := Painter{Theme: livediff.DarkTheme}
	uisnapshot.Assert(t, "testdata/snapshots/search_output_selected_tail.txt", strings.Join(p.Block(block, 96), "\n")+"\n")
}

func BenchmarkSearchOutputRetainedSelectedTail(b *testing.B) {
	rows := make([]string, 1200)
	for i := range rows {
		rows[i] = fmt.Sprintf("internal/router/provider_%d.go:%d:func answer%d() int { return 42 }", i%40, i+1, i)
	}
	block := selectedOutputBlock(Block{Verb: "Search"}, rows, len(rows)-8)
	for _, selected := range []bool{true, false} {
		name := "full-retained-context"
		if selected {
			name = "selected-tail"
		}
		b.Run(name, func(b *testing.B) {
			p := Painter{Theme: livediff.DarkTheme}
			retained := block.Output.View().Lines
			paint := func() []string {
				if selected {
					return p.tailColors(block)
				}
				return p.outputColors(block, retained)[block.TailOmitted:]
			}
			paint()
			b.ReportAllocs()
			for b.Loop() {
				if got := paint(); len(got) != len(block.Tail) {
					b.Fatal("selected tail lost rows")
				}
			}
		})
	}
}

func TestOutputTailLayoutPreservesBoundedText(t *testing.T) {
	for _, content := range []string{
		strings.Repeat("long output ", 2000),
		strings.Repeat("界 é 👩‍💻 ", 2000),
	} {
		block := selectedOutputBlock(Block{Kind: "op", Verb: "Run", Code: "producer"}, []string{"old", "", content, "done"}, 1)
		plain := Painter{LayoutOnly: true, Theme: livediff.DarkTheme}
		colored := Painter{Theme: livediff.DarkTheme}
		if got := plain.tailColors(block); !slices.Equal(got, block.Tail) {
			t.Fatal("layout changed the observed tail")
		}
		if got := colored.tailColors(block); !slices.Equal(got, block.Tail) {
			t.Fatal("unhighlighted retained output changed the observed tail")
		}
		for _, width := range []int{22, 80} {
			got := plain.Block(block, width)
			want := colored.Block(block, width)
			if ansi.Strip(strings.Join(got, "\n")) != ansi.Strip(strings.Join(want, "\n")) {
				t.Fatal("layout and decorated output geometry differ")
			}
		}
	}
}

func BenchmarkOutputTailLongRetainedLine(b *testing.B) {
	block := selectedOutputBlock(Block{Kind: "op", Verb: "Run"}, []string{strings.Repeat("large untyped output ", 800)}, 0)
	for _, layout := range []bool{true, false} {
		b.Run(fmt.Sprintf("layout-%t", layout), func(b *testing.B) {
			p := Painter{Theme: livediff.DarkTheme, LayoutOnly: layout}
			b.ReportAllocs()
			for b.Loop() {
				if got := p.tailColors(block); !slices.Equal(got, block.Tail) {
					b.Fatal("tail changed")
				}
			}
		})
	}
}
