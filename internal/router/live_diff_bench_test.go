package router

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkLiveDiffWrappedRender(b *testing.B) {
	for _, rows := range []int{100, 1000} {
		chunk := liveDiffHighlightChunk("edit", "file.go",
			fmt.Sprintf("@@ -0,0 +1,%d @@\n", rows)+
				strings.Repeat("+var value = \""+strings.Repeat("abcdefgh ", 15)+"\"\n", rows), true)
		files := []liveDiffFile{{path: "file.go", chunks: []liveDiffChunk{chunk}}}
		for _, warm := range []bool{false, true} {
			b.Run(fmt.Sprintf("%d/warm=%t", rows, warm), func(b *testing.B) {
				var renderer liveDiffRenderer
				if warm {
					if _, err := renderer.render(b.Context(), liveDiffDarkTheme, files, "", 100, 0, chunk); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportAllocs()
				for b.Loop() {
					if !warm {
						renderer = liveDiffRenderer{}
					}
					if _, err := renderer.render(b.Context(), liveDiffDarkTheme, files, "", 100, 0, chunk); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
