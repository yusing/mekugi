package terminalview

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestPaneUnchangedPaintWork(t *testing.T) {
	for _, count := range []int{1, 128} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			pane := NewPane("", diffrender.DarkTheme)
			view := presentation.ReviewView{Revision: 1,
				Publication: (presentation.Publication{}).With(viewportSyntaxFiles(count, "go"))}
			pane.PublishReview(view)
			var output bytes.Buffer
			ctx := t.Context()
			if err := pane.Paint(ctx, &output, 120, 12); err != nil {
				t.Fatal(err)
			}
			lines := &pane.rendering.Lines[0]
			output.Reset()
			allocs := testing.AllocsPerRun(10, func() {
				pane.PublishReview(view)
				if err := pane.Paint(ctx, &output, 120, 12); err != nil {
					t.Fatal(err)
				}
			})
			if allocs != 0 || output.Len() != 0 {
				t.Fatalf("unchanged paint: %.0f allocations, %d output bytes", allocs, output.Len())
			}
			// Identical republication and a replaced surface must redraw without
			// rebuilding the document.
			view.Revision++
			pane.PublishReview(view)
			pane.Invalidate()
			if err := pane.Paint(ctx, &output, 120, 12); err != nil {
				t.Fatal(err)
			}
			if output.Len() == 0 || &pane.rendering.Lines[0] != lines {
				t.Fatal("surface invalidation skipped redraw or rebuilt unchanged layout")
			}
		})
	}
}
