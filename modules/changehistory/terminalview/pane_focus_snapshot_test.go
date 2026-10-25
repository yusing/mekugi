package terminalview

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotLiveDiffFocus(t *testing.T) {
	for _, width := range []int{70, 120} {
		for _, focused := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d-files-%t", width, focused), func(t *testing.T) {
				c := liveDiffChangesController(t, width, 18, []presentation.Chunk{
					liveDiffCapture("one", "a.go", 1, "old\n", "new\n", presentation.Origin{}),
					liveDiffCapture("two", "b.go", 2, "before\n", "after\n", presentation.Origin{}),
				})
				c.navigation.Focused = focused
				var output bytes.Buffer
				c.paintTo(t, &output)
				left, right := c.Title()
				rows := []string{left + " | " + right}
				for row := 1; row <= 18; row++ {
					rows = append(rows, liveDiffFrameRow(output.String(), row))
				}
				uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/live-diff-focus-%d-%t.txt", width, focused), strings.Join(rows, "\n"))
			})
		}
	}
}

func TestUISnapshotSavedDiffCapacity(t *testing.T) {
	pane := liveDiffChangesController(t, 80, 6, []presentation.Chunk{
		liveDiffCapture("one", "a.go", 1, "old\n", "new\n", presentation.Origin{}),
	})
	pane.navigation.Focused = true
	pane.PublishReview(presentation.ReviewView{Revision: 1, Workspace: pane.Workspace(),
		Failure: "Saved Diff exceeds the native publication capacity; use mchanges with a narrower range."})
	var output bytes.Buffer
	pane.paintTo(t, &output)
	left, right := pane.Title()
	rows := []string{left + " | " + right}
	for row := 1; row <= 6; row++ {
		rows = append(rows, liveDiffFrameRow(output.String(), row))
	}
	uisnapshot.Assert(t, "testdata/snapshots/saved-diff-capacity.txt", strings.Join(rows, "\n"))
}
