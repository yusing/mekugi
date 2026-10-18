package launch

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/yusing/mekugi/modules/changehistory/presentation"
	changeview "github.com/yusing/mekugi/modules/changehistory/terminalview"
)

func BenchmarkLiveDiffWrappedRender(b *testing.B) {
	for _, rows := range []int{100, 1000} {
		chunk := liveDiffHighlightChunk("edit", "file.go",
			fmt.Sprintf("@@ -0,0 +1,%d @@\n", rows)+
				strings.Repeat("+var value = \""+strings.Repeat("abcdefgh ", 15)+"\"\n", rows))
		files := []presentation.File{{Path: "file.go", Chunks: []presentation.Chunk{chunk}}}
		for _, warm := range []bool{false, true} {
			b.Run(fmt.Sprintf("%d/warm=%t", rows, warm), func(b *testing.B) {
				var renderer changeview.Renderer
				if warm {
					if _, err := renderer.Render(b.Context(), diffrender.DarkTheme, files, "", 100); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportAllocs()
				for b.Loop() {
					if !warm {
						renderer = changeview.Renderer{}
					}
					if _, err := renderer.Render(b.Context(), diffrender.DarkTheme, files, "", 100); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
