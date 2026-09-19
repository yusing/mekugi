package router

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

func TestLiveDiffSyntaxCacheMatchesColdRender(t *testing.T) {
	var renderer liveDiffRenderer
	for _, theme := range []liveDiffTheme{liveDiffTerminalTheme, liveDiffDarkTheme, liveDiffLightTheme} {
		for _, path := range []string{"file.go", "file.txt"} {
			for _, value := range []string{"old", "changed"} {
				chunk := liveDiffHighlightChunk("edit", path,
					"@@ -1,3 +1,3 @@\n // "+strings.Repeat("界 é ", 30)+"\n-old := \"old\"\n+next := \""+value+"\"\n tail\n\\ No newline at end of file\n", true)
				chunk.highlighted = true
				files := []liveDiffFile{{path: path, highlighted: true, chunks: []liveDiffChunk{chunk}}}
				for _, width := range []int{90, 22, 1, 7, 90} {
					got, err := renderer.render(t.Context(), theme, files, "", width, 0, chunk)
					if err != nil {
						t.Fatal(err)
					}
					want, err := new(liveDiffRenderer).render(t.Context(), theme, files, "", width, 0, chunk)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("cached render changed theme=%v path=%s source=%s width=%d: %v", theme, path, value, width, err)
					}
				}
			}
		}
	}
}

func TestLiveDiffSyntaxCacheReuseAndBounds(t *testing.T) {
	var renderer liveDiffRenderer
	const source = "var value = \"retained\"\n"
	first, err := renderer.colorSource(t.Context(), liveDiffDarkTheme, "file.go", source)
	if err != nil {
		t.Fatal(err)
	}
	size := renderer.syntaxBytes
	second, err := renderer.colorSource(t.Context(), liveDiffDarkTheme, "file.go", source)
	if err != nil || &first[0] != &second[0] || renderer.syntaxBytes != size {
		t.Fatal("identical source was not reused")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, path := range []string{"file.go", "unknown.extension"} {
		for _, text := range []string{source, "changed\n"} {
			if _, err := renderer.colorSource(ctx, liveDiffDarkTheme, path, text); !errors.Is(err, context.Canceled) {
				t.Fatalf("cache hit or miss ignored cancellation: %v", err)
			}
		}
	}
	if renderer.syntaxBytes != size || len(renderer.syntax) != 1 || len(renderer.lexers) != 1 {
		t.Fatal("cancellation changed cached state")
	}

	// Entry count bounds tiny inputs; byte accounting bounds large source.
	for i := range maxLiveDiffSyntaxCacheEntries + 1 {
		_, err := renderer.colorSource(t.Context(), liveDiffDarkTheme, "file.txt", fmt.Sprintf("%d\n", i))
		if err != nil || len(renderer.syntax) > maxLiveDiffSyntaxCacheEntries || len(renderer.lexers) > len(renderer.syntax) {
			t.Fatalf("entry limit: %d, %v", len(renderer.syntax), err)
		}
	}
	if _, exists := renderer.syntax[liveDiffSyntaxKey{liveDiffDarkTheme, "file.go", source}]; exists {
		t.Fatal("entry limit did not evict old source")
	}
	large := strings.Repeat("x", maxLiveDiffSyntaxCacheBytes/4)
	for i := range 3 {
		_, err := renderer.colorSource(t.Context(), liveDiffDarkTheme, "file.txt", large+fmt.Sprint(i))
		if err != nil || renderer.syntaxBytes > maxLiveDiffSyntaxCacheBytes {
			t.Fatalf("byte limit: %d, %v", renderer.syntaxBytes, err)
		}
	}
	if len(renderer.syntax) != 1 || len(renderer.lexers) != 0 {
		t.Fatal("byte limit did not evict old source and lexer selections")
	}
	// The backing slice for blank rows also consumes cache storage.
	blankRows := strings.Repeat("\n", maxLiveDiffSyntaxCacheBytes/8)
	size = renderer.syntaxBytes
	_, err = renderer.colorSource(t.Context(), liveDiffDarkTheme, "file.txt", blankRows)
	if err != nil || renderer.syntaxBytes != size || len(renderer.syntax) != 1 {
		t.Fatal("blank row headers escaped the cache byte limit")
	}
	size = renderer.syntaxBytes
	_, err = renderer.colorSource(t.Context(), liveDiffDarkTheme, "file.txt", strings.Repeat("x", maxLiveDiffSyntaxCacheBytes))
	if err != nil || renderer.syntaxBytes != size || len(renderer.syntax) != 1 {
		t.Fatal("oversized source displaced the bounded cache")
	}
}

func TestLiveDiffOutputStringLimit(t *testing.T) {
	var output liveDiffOutput
	output.Builder.Grow(maxChangeReadBytes)
	output.Builder.WriteString(strings.Repeat("x", maxChangeReadBytes-1))
	if _, err := output.WriteString("y"); err != nil {
		t.Fatal(err)
	}
	if _, err := output.WriteString("z"); err == nil || output.Len() != maxChangeReadBytes {
		t.Fatal("string write exceeded output limit")
	}
}

func BenchmarkLiveDiffChangingSource(b *testing.B) {
	sources := make([]string, maxLiveDiffSyntaxCacheEntries+1)
	for i := range sources {
		sources[i] = fmt.Sprintf("var value = %q\n", fmt.Sprint(i))
	}
	for _, path := range []string{"file.txt", "file.go"} {
		b.Run(path, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var renderer liveDiffRenderer
				for _, source := range sources {
					if _, err := renderer.colorSource(b.Context(), liveDiffDarkTheme, path, source); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

type liveDiffCountingLexer struct {
	chroma.Lexer
	configCalls int
}

func (l *liveDiffCountingLexer) Config() *chroma.Config {
	l.configCalls++
	return l.Lexer.Config()
}

func TestLiveDiffLexerCacheSelections(t *testing.T) {
	original := lexers.GlobalLexerRegistry
	lexers.GlobalLexerRegistry = chroma.NewLexerRegistry()
	t.Cleanup(func() { lexers.GlobalLexerRegistry = original })
	lexer := &liveDiffCountingLexer{Lexer: chroma.MustNewLexer(&chroma.Config{
		Name: "fixture", Filenames: []string{"*.fixture", "Buildfile"},
	}, func() chroma.Rules {
		return chroma.Rules{"root": {{Pattern: `(?s).+`, Type: chroma.Keyword}}}
	})}
	lexers.Register(lexer)

	t.Run("reuse across sources and themes", func(t *testing.T) {
		var renderer liveDiffRenderer
		for _, theme := range []liveDiffTheme{liveDiffTerminalTheme, liveDiffDarkTheme, liveDiffLightTheme} {
			// Extensionless names must remain distinct; unknown selections
			// must be reused just like known lexers.
			for _, path := range []string{"file.fixture", "Buildfile", "Otherfile", "unknown.extension"} {
				for _, source := range []string{"first\n", "changed\n"} {
					_, cached := renderer.lexers[path]
					before := lexer.configCalls
					got, err := renderer.colorSource(t.Context(), theme, path, source)
					calls := lexer.configCalls - before
					want, coldErr := liveDiffColorSource(t.Context(), theme, path, source)
					if err != nil || coldErr != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("cached selection changed %s: got %q, want %q, errors %v %v", path, got, want, err, coldErr)
					}
					selected, exists := renderer.lexers[path]
					if !exists {
						t.Fatalf("selection for %s was not cached", path)
					}
					if cached {
						wantCalls := 0
						if selected != nil {
							wantCalls = 1 // Rendering checks the selected lexer's name.
						}
						if calls != wantCalls {
							t.Fatalf("cached %s rescanned the registry: %d Config calls, want %d", path, calls, wantCalls)
						}
					}
					if unknown := path == "Otherfile" || path == "unknown.extension"; unknown != (selected == nil) {
						t.Fatalf("wrong cached selection for %s: %v", path, selected)
					}
				}
			}
		}
	})

	t.Run("entry eviction", func(t *testing.T) {
		var renderer liveDiffRenderer
		for i := range maxLiveDiffSyntaxCacheEntries + 1 {
			path := fmt.Sprintf("file-%d.fixture", i)
			if _, err := renderer.colorSource(t.Context(), liveDiffDarkTheme, path, "value\n"); err != nil {
				t.Fatal(err)
			}
			if len(renderer.lexers) != len(renderer.syntax) || len(renderer.lexers) > maxLiveDiffSyntaxCacheEntries {
				t.Fatal("lexer selections escaped syntax entry bounds")
			}
		}
		if len(renderer.lexers) != 1 || renderer.lexers["file-0.fixture"] != nil {
			t.Fatal("entry limit did not evict old lexer selections")
		}
	})

	t.Run("oversized and empty sources skip matching", func(t *testing.T) {
		var renderer liveDiffRenderer
		for _, source := range []string{"", strings.Repeat("x", maxLiveDiffSyntaxBytes+1)} {
			before := lexer.configCalls
			got, err := renderer.colorSource(t.Context(), liveDiffDarkTheme, "file.fixture", source)
			want, coldErr := liveDiffColorSource(t.Context(), liveDiffDarkTheme, "file.fixture", source)
			if err != nil || coldErr != nil || !reflect.DeepEqual(got, want) || lexer.configCalls != before || len(renderer.lexers) != 0 {
				t.Fatal("plain fallback consulted or cached a lexer")
			}
		}
	})

	t.Run("match panic falls back without caching selection", func(t *testing.T) {
		lexer.Lexer.Config().Filenames = []string{"["}
		var renderer liveDiffRenderer
		for _, source := range []string{"first\n", "changed\n"} {
			got, err := renderer.colorSource(t.Context(), liveDiffDarkTheme, "file.fixture", source)
			if err != nil || strings.Join(got, "\n")+"\n" != source || len(renderer.lexers) != 0 {
				t.Fatalf("match panic leaked a partial selection: %q %v", got, err)
			}
		}
	})
}
