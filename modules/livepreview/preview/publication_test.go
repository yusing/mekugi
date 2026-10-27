package preview

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestBoundPreviewEscapingAndLargeInput(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", 47<<10), strings.Repeat("\x00", 10<<10), strings.Repeat("界", 64<<10)} {
		input := Preview{ID: "preview", Input: text}
		before := responses.MustMarshalJSON(input)
		got := boundPreview(input)
		if len(responses.MustMarshalJSON(got)) > 48<<10 || !utf8.ValidString(got.Input) || !strings.HasSuffix(text, got.Input) {
			t.Fatal("preview exceeded bound or changed source suffix")
		}
		if got.Truncated != (len(before) > 48<<10) || !got.Truncated && got.Input != text {
			t.Fatal("preview truncation changed")
		}
	}
}

func BenchmarkBoundPreview(b *testing.B) {
	for _, size := range []int{16 << 10, 256 << 10, 2 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			p := Preview{ID: "a", Files: []diffmodel.ReviewFile{{BeforePath: "x.go", AfterPath: "x.go", Diff: "--- x.go\n+++ x.go\n@@ -1 +1 @@\n" + strings.Repeat("+new\n", size/5)}}}
			b.ReportAllocs()
			for b.Loop() {
				got := boundPreview(p)
				if got.ID != p.ID || size > 48<<10 && !got.Truncated {
					b.Fatal("preview identity or clipping changed")
				}
			}
		})
	}
}
