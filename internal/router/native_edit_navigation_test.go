package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestNativeEditClickOpensExactCapturedDiffDialog(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		for _, repeated := range []bool{false, true} {
			t.Run(fmt.Sprint(grouped, repeated), func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				captures := []livediff.Chunk{
					liveDiffCapture("wrong", "file20.go", 1, "wrong-old\n", "wrong-new\n", livediff.Origin{Change: "amber1", Caller: "/root", Source: "apply_patch"}),
					liveDiffCapture("target", "file20.go", 2, "target-old\n", "target-new\n", livediff.Origin{Change: "amber20", Caller: "/root", Source: "apply_patch"}),
				}
				c := liveDiffChangesController(t, 120, 28, captures)
				u.shell.diff = c
				c.data = newLiveDiffData()
				c.data.order = []string{"wrong", "target"}
				c.data.attempts["wrong"] = liveDiffAttempt{thread: "child", correlation: "call\x000", change: "amber1", chunks: captures[:1]}
				attempt := liveDiffAttempt{thread: "main", correlation: "call\x000", change: "amber20", chunks: captures[1:]}
				if grouped {
					attempt.correlation = "exec-carrier"
					attempt.receipt = &capturedActivityEdit{thread: "main", calls: []string{"call"}}
				}
				c.data.attempts["target"] = attempt
				path := "earlier.go"
				if repeated {
					path = "file20.go"
				}
				u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "tool", Text: "Edit `" + path + "` +1 -1 · apply_patch", native: &liveActivityNativeItem{thread: "main", item: "earlier"}}, {Seq: 2, Agent: "Main", Kind: "tool", Text: "Edit `file20.go` +1 -1 · apply_patch", native: &liveActivityNativeItem{thread: "main", item: "call"}}}})
				u.shell.side, u.shell.diffOpen, u.shell.focus = true, true, 1
				c.filterCaller("/root")
				c.navigation.Changes.Query = "amber1"
				c.navigation.ChangesTab = true
				c.openChange("amber1", 0)
				c.refreshChanges()
				if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
					t.Fatal(err)
				}
				if !u.shell.openActivityEdit(u.view, 2, "file20.go") || u.shell.output == nil {
					t.Fatal("edit did not open dialog")
				}
				if !u.shell.diffOpen || u.shell.focus != 1 || c.navigation.Changes.Query != "amber1" || c.navigation.Changes.Target.Change != "amber1" {
					t.Fatal("edit dialog changed live diff navigation")
				}
				if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
					t.Fatal(err)
				}
				page := u.shell.output.laid.Text
				if !strings.Contains(page, "target-new") || strings.Contains(page, "wrong-new") {
					t.Fatalf("wrong invocation diff: %q", page)
				}
				u.shell.outputKey("\x1b")
				if u.shell.output != nil || c.navigation.Changes.Query != "amber1" || c.navigation.Changes.Target.Change != "amber1" {
					t.Fatal("closing edit dialog changed prior diff state")
				}
			})
		}
	}
}

func TestNativeEditDialogShowsHistoricalHunkNotLaterCapture(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	before := liveDiffLinesFile("target", 100)
	after := strings.Replace(before, "target_line_80", "clicked_hunk", 1)
	later := strings.Repeat("later_hunk\n", 100) + after
	captures := []livediff.Chunk{
		liveDiffCapture("clicked", "target.go", 1, before, after, livediff.Origin{Change: "amber1", Caller: "/root", Source: "apply_patch"}),
		liveDiffCapture("later", "target.go", 2, after, later, livediff.Origin{Change: "amber2", Caller: "/root", Source: "apply_patch"}),
	}
	c := liveDiffChangesController(t, 120, 24, captures)
	u.shell.diff = c
	c.data = newLiveDiffData()
	c.data.order = []string{"clicked"}
	c.data.attempts["clicked"] = liveDiffAttempt{thread: "main", correlation: "call\x000", change: "amber1", chunks: captures[:1]}
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "tool", Text: "Edit `target.go` +1 -1 · apply_patch", native: &liveActivityNativeItem{thread: "main", item: "call"}}}})
	if !u.shell.openActivityEdit(u.view, 1, "target.go") {
		t.Fatal("edit dialog failed")
	}
	if err := u.paint(&bytes.Buffer{}, 120, 24); err != nil {
		t.Fatal(err)
	}
	if got := u.shell.output.laid.Text; !strings.Contains(got, "clicked_hunk") || strings.Contains(got, "later_hunk") {
		t.Fatalf("dialog did not show historical capture: %q", got)
	}
	u.shell.outputKey("\x1b")
	c.frame(t)
	if !strings.Contains(strings.Join(c.lines, "\n"), "later_hunk") {
		t.Fatal("closing dialog lost later capture")
	}
}
