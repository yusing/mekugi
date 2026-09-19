package livediff

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
	var renderer Renderer
	for _, theme := range []Theme{TerminalTheme, DarkTheme, LightTheme} {
		for _, path := range []string{"file.go", "file.txt"} {
			for _, value := range []string{"old", "changed"} {
				chunk := testThemeChunk("edit", path,
					"@@ -1,3 +1,3 @@\n // "+strings.Repeat("界 é ", 30)+"\n-old := \"old\"\n+next := \""+value+"\"\n tail\n\\ No newline at end of file\n", true)
				chunk.Highlighted = true
				files := []File{{Path: path, Highlighted: true, Chunks: []Chunk{chunk}}}
				for _, width := range []int{90, 22, 1, 7, 90} {
					got, err := renderer.Render(t.Context(), theme, files, "", width, 0, chunk)
					if err != nil {
						t.Fatal(err)
					}
					want, err := new(Renderer).Render(t.Context(), theme, files, "", width, 0, chunk)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("cached render changed theme=%v path=%s source=%s width=%d: %v", theme, path, value, width, err)
					}
				}
			}
		}
	}
}

func TestLiveDiffSyntaxCacheReuseAndBounds(t *testing.T) {
	var renderer Renderer
	const source = "var value = \"retained\"\n"
	first, err := renderer.ColorSource(t.Context(), DarkTheme, "file.go", source)
	if err != nil {
		t.Fatal(err)
	}
	size := renderer.syntaxBytes
	second, err := renderer.ColorSource(t.Context(), DarkTheme, "file.go", source)
	if err != nil || &first[0] != &second[0] || renderer.syntaxBytes != size {
		t.Fatal("identical source was not reused")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, path := range []string{"file.go", "unknown.extension"} {
		for _, text := range []string{source, "changed\n"} {
			if _, err := renderer.ColorSource(ctx, DarkTheme, path, text); !errors.Is(err, context.Canceled) {
				t.Fatalf("cache hit or miss ignored cancellation: %v", err)
			}
		}
	}
	if renderer.syntaxBytes != size || len(renderer.syntax) != 1 || len(renderer.lexers) != 1 {
		t.Fatal("cancellation changed cached state")
	}

	// Entry count bounds tiny inputs; byte accounting bounds large source.
	for i := range maxSyntaxCacheEntries + 1 {
		_, err := renderer.ColorSource(t.Context(), DarkTheme, "file.txt", fmt.Sprintf("%d\n", i))
		if err != nil || len(renderer.syntax) > maxSyntaxCacheEntries || len(renderer.lexers) > len(renderer.syntax) {
			t.Fatalf("entry limit: %d, %v", len(renderer.syntax), err)
		}
	}
	if _, exists := renderer.syntax[syntaxKey{DarkTheme, "file.go", source}]; exists {
		t.Fatal("entry limit did not evict old source")
	}
	large := strings.Repeat("x", maxSyntaxCacheBytes/4)
	for i := range 3 {
		_, err := renderer.ColorSource(t.Context(), DarkTheme, "file.txt", large+fmt.Sprint(i))
		if err != nil || renderer.syntaxBytes > maxSyntaxCacheBytes {
			t.Fatalf("byte limit: %d, %v", renderer.syntaxBytes, err)
		}
	}
	if len(renderer.syntax) != 1 || len(renderer.lexers) != 0 {
		t.Fatal("byte limit did not evict old source and lexer selections")
	}
	// The backing slice for blank rows also consumes cache storage.
	blankRows := strings.Repeat("\n", maxSyntaxCacheBytes/8)
	size = renderer.syntaxBytes
	_, err = renderer.ColorSource(t.Context(), DarkTheme, "file.txt", blankRows)
	if err != nil || renderer.syntaxBytes != size || len(renderer.syntax) != 1 {
		t.Fatal("blank row headers escaped the cache byte limit")
	}
	size = renderer.syntaxBytes
	_, err = renderer.ColorSource(t.Context(), DarkTheme, "file.txt", strings.Repeat("x", maxSyntaxCacheBytes))
	if err != nil || renderer.syntaxBytes != size || len(renderer.syntax) != 1 {
		t.Fatal("oversized source displaced the bounded cache")
	}
}

func TestLiveDiffOutputStringLimit(t *testing.T) {
	var output Output
	output.Builder.Grow(MaxSourceBytes)
	output.Builder.WriteString(strings.Repeat("x", MaxSourceBytes-1))
	if _, err := output.WriteString("y"); err != nil {
		t.Fatal(err)
	}
	if _, err := output.WriteString("z"); err == nil || output.Len() != MaxSourceBytes {
		t.Fatal("string write exceeded output limit")
	}
}

func BenchmarkLiveDiffChangingSource(b *testing.B) {
	sources := make([]string, maxSyntaxCacheEntries+1)
	for i := range sources {
		sources[i] = fmt.Sprintf("var value = %q\n", fmt.Sprint(i))
	}
	for _, path := range []string{"file.txt", "file.go"} {
		b.Run(path, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var renderer Renderer
				for _, source := range sources {
					if _, err := renderer.ColorSource(b.Context(), DarkTheme, path, source); err != nil {
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
		var renderer Renderer
		for _, theme := range []Theme{TerminalTheme, DarkTheme, LightTheme} {
			// Extensionless names must remain distinct; unknown selections
			// must be reused just like known lexers.
			for _, path := range []string{"file.fixture", "Buildfile", "Otherfile", "unknown.extension"} {
				for _, source := range []string{"first\n", "changed\n"} {
					_, cached := renderer.lexers[path]
					before := lexer.configCalls
					got, err := renderer.ColorSource(t.Context(), theme, path, source)
					calls := lexer.configCalls - before
					want, coldErr := new(Renderer).ColorSource(t.Context(), theme, path, source)
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
		var renderer Renderer
		for i := range maxSyntaxCacheEntries + 1 {
			path := fmt.Sprintf("file-%d.fixture", i)
			if _, err := renderer.ColorSource(t.Context(), DarkTheme, path, "value\n"); err != nil {
				t.Fatal(err)
			}
			if len(renderer.lexers) != len(renderer.syntax) || len(renderer.lexers) > maxSyntaxCacheEntries {
				t.Fatal("lexer selections escaped syntax entry bounds")
			}
		}
		if len(renderer.lexers) != 1 || renderer.lexers["file-0.fixture"] != nil {
			t.Fatal("entry limit did not evict old lexer selections")
		}
	})

	t.Run("oversized and empty sources skip matching", func(t *testing.T) {
		var renderer Renderer
		for _, source := range []string{"", strings.Repeat("x", MaxSyntaxBytes+1)} {
			before := lexer.configCalls
			got, err := renderer.ColorSource(t.Context(), DarkTheme, "file.fixture", source)
			want, coldErr := new(Renderer).ColorSource(t.Context(), DarkTheme, "file.fixture", source)
			if err != nil || coldErr != nil || !reflect.DeepEqual(got, want) || lexer.configCalls != before || len(renderer.lexers) != 0 {
				t.Fatal("plain fallback consulted or cached a lexer")
			}
		}
	})

	t.Run("match panic falls back without caching selection", func(t *testing.T) {
		lexer.Lexer.Config().Filenames = []string{"["}
		var renderer Renderer
		for _, source := range []string{"first\n", "changed\n"} {
			got, err := renderer.ColorSource(t.Context(), DarkTheme, "file.fixture", source)
			if err != nil || strings.Join(got, "\n")+"\n" != source || len(renderer.lexers) != 0 {
				t.Fatalf("match panic leaked a partial selection: %q %v", got, err)
			}
		}
	})
}
