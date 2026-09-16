package router

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
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
	if _, err := renderer.colorSource(ctx, liveDiffDarkTheme, "file.go", source); !errors.Is(err, context.Canceled) {
		t.Fatalf("cache hit ignored cancellation: %v", err)
	}

	// Entry count bounds tiny inputs; byte accounting bounds large source.
	for i := range maxLiveDiffSyntaxCacheEntries + 1 {
		_, err := renderer.colorSource(t.Context(), liveDiffDarkTheme, "file.txt", fmt.Sprintf("%d\n", i))
		if err != nil || len(renderer.syntax) > maxLiveDiffSyntaxCacheEntries {
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
	if len(renderer.syntax) != 1 {
		t.Fatal("byte limit did not evict old source")
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
