package router

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
)

func BenchmarkLiveDiffWrappedRender(b *testing.B) {
	for _, rows := range []int{100, 1000} {
		chunk := liveDiffHighlightChunk("edit", "file.go",
			fmt.Sprintf("@@ -0,0 +1,%d @@\n", rows)+
				strings.Repeat("+var value = \""+strings.Repeat("abcdefgh ", 15)+"\"\n", rows))
		files := []livediff.File{{Path: "file.go", Chunks: []livediff.Chunk{chunk}}}
		for _, warm := range []bool{false, true} {
			b.Run(fmt.Sprintf("%d/warm=%t", rows, warm), func(b *testing.B) {
				var renderer livediff.Renderer
				if warm {
					if _, err := renderer.Render(b.Context(), livediff.DarkTheme, files, "", 100); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportAllocs()
				for b.Loop() {
					if !warm {
						renderer = livediff.Renderer{}
					}
					if _, err := renderer.Render(b.Context(), livediff.DarkTheme, files, "", 100); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
