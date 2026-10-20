package terminalview

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func navigatorScrollPane(t *testing.T, width int, changes bool) *Pane {
	t.Helper()
	var captures []presentation.Chunk
	for i := range 60 {
		name := fmt.Sprintf("file%02d.go", i)
		captures = append(captures, liveDiffCapture(name, name, uint64(i+1), "old\n", "new\n",
			presentation.Origin{Change: fmt.Sprintf("amber%d", i+1), Caller: "/root"}))
	}
	c := liveDiffChangesController(t, width, 18, captures)
	c.Key("s")
	if changes {
		c.Key("\t")
	}
	c.frame(t)
	return c
}

func TestNavigatorWheelPastSelection(t *testing.T) {
	for _, width := range []int{70, 120} {
		for _, changes := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d-changes-%t", width, changes), func(t *testing.T) {
				c := navigatorScrollPane(t, width, changes)
				top, cursor := &c.navigation.Top, &c.navigation.Cursor
				if changes {
					top, cursor = &c.navigation.Changes.Top, &c.navigation.Changes.Cursor
				}
				for range 20 {
					c.Key("\x1b[B")
					c.frame(t)
				}
				selected, offset, selectedCursor := c.view.Selected, c.offset, *cursor
				for _, action := range []byte{'k', 'j'} {
					for range 30 {
						expected := *top + 1
						if action == 'k' {
							expected = max(0, *top-1)
						}
						c.Mouse(action, 3, 1)
						c.frame(t)
						if *top != expected || *cursor != selectedCursor || c.view.Selected != selected || c.offset != offset {
							t.Fatalf("wheel %c: top=%d, want %d; cursor=%d/%d selection=%d/%d offset=%d/%d", action,
								*top, expected, *cursor, selectedCursor, c.view.Selected, selected, c.offset, offset)
						}
					}
					if *cursor >= *top && *cursor < *top+c.navRows-2 {
						t.Fatalf("wheel %c kept selection visible: cursor=%d top=%d", action, *cursor, *top)
					}
				}
				// A publication may refresh the diff without returning to its selected row.
				files := append([]presentation.File(nil), c.view.Files...)
				files = append(files, presentation.GroupCaptures([]presentation.Chunk{
					liveDiffCapture("new", "zz-new.go", 61, "old\n", "new\n", presentation.Origin{Change: "amber61", Caller: "/root"}),
				})...)
				var publication presentation.Publication
				c.Update(publication.With(files), "")
				c.frame(t)
				if *top != 30 || *cursor != selectedCursor || c.view.Selected != selected || c.offset != offset {
					t.Fatal("publication moved the scrolled navigator or selected content")
				}
				c.Key("\r")
				c.frame(t)
				selected, offset, selectedCursor = c.view.Selected, c.offset, *cursor
				for range c.navRows {
					expected := *top + 1
					c.Mouse('j', 3, 1)
					c.frame(t)
					if *top != expected || *cursor != selectedCursor || c.view.Selected != selected || c.offset != offset || c.navigation.Focused {
						t.Fatal("wheel moved selection, content or focus instead of the unfocused navigator")
					}
				}
				c.Key("\x1b")
				c.Key("\x1b[B")
				c.frame(t)
				if *cursor < *top || *cursor >= *top+c.navRows-2 {
					t.Fatal("keyboard navigation did not reveal its destination")
				}
				c.Key("\x1b[6~")
				c.frame(t)
				if err := c.Paint(t.Context(), io.Discard, width, 12); err != nil {
					t.Fatal(err)
				}
				if *cursor < *top || *cursor >= *top+c.navRows-2 {
					t.Fatal("resize hid the focused entry")
				}
				c.Key("n")
				c.frame(t)
				if n := &c.navigation; n.Cursor < n.Top || n.Cursor >= n.Top+c.navRows-2 || n.Entries[n.Cursor].File != c.view.Selected {
					t.Fatal("explicit file jump did not reveal its destination")
				}
			})
		}
	}
}

func TestUISnapshotNavigatorScrolledPastSelection(t *testing.T) {
	for _, changes := range []bool{false, true} {
		t.Run(fmt.Sprintf("changes-%t", changes), func(t *testing.T) {
			c := navigatorScrollPane(t, 70, changes)
			for range 10 {
				c.Mouse('j', 3, 1)
				c.frame(t)
			}
			var output bytes.Buffer
			c.paintTo(t, &output)
			var rows []string
			for row := 1; row <= c.lastHeight; row++ {
				rows = append(rows, liveDiffFrameRow(output.String(), row))
			}
			uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/navigator-scrolled-changes-%t.txt", changes), strings.Join(rows, "\n"))
		})
	}
}
