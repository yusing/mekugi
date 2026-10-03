package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotLiveDiffFocus(t *testing.T) {
	for _, width := range []int{70, 120} {
		for _, focused := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d-files-%t", width, focused), func(t *testing.T) {
				c := liveDiffChangesController(t, width, 18, []livediff.Chunk{
					liveDiffCapture("one", "a.go", 1, "old\n", "new\n", livediff.Origin{}),
					liveDiffCapture("two", "b.go", 2, "before\n", "after\n", livediff.Origin{}),
				})
				c.native = true
				c.navigation.Focused = focused
				var output bytes.Buffer
				c.stdout = &output
				c.frame(t)
				left, right := c.nativeTitle()
				rows := []string{left + " | " + right}
				for row := 1; row <= 18; row++ {
					rows = append(rows, liveDiffFrameRow(output.String(), row))
				}
				uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/live-diff-focus-%d-%t.txt", width, focused), strings.Join(rows, "\n"))
			})
		}
	}
}
