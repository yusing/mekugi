package livediff

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffrender"
)

func TestLiveDiffSyntaxCacheMatchesColdRender(t *testing.T) {
	var renderer Renderer
	for _, theme := range []diffrender.Theme{diffrender.TerminalTheme, diffrender.DarkTheme, diffrender.LightTheme} {
		for _, path := range []string{"file.go", "file.txt"} {
			for _, value := range []string{"old", "changed"} {
				chunk := testThemeChunk("edit", path,
					"@@ -1,3 +1,3 @@\n // "+strings.Repeat("界 é ", 30)+"\n-old := \"old\"\n+next := \""+value+"\"\n tail\n\\ No newline at end of file\n")
				chunk.Highlighted = true
				files := []File{{Path: path, Highlighted: true, Chunks: []Chunk{chunk}}}
				for _, width := range []int{90, 22, 1, 7, 90} {
					got, err := renderer.Render(t.Context(), theme, files, "", width)
					if err != nil {
						t.Fatal(err)
					}
					want, err := new(Renderer).Render(t.Context(), theme, files, "", width)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("cached render changed theme=%v path=%s source=%s width=%d: %v", theme, path, value, width, err)
					}
				}
			}
		}
	}
}
