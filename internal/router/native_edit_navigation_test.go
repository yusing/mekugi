package router

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestNativeEditClickOpensBranchedDiff(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		for _, repeated := range []bool{false, true} {
			t.Run(fmt.Sprint(grouped, repeated), func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				var captures []livediff.Chunk
				for i := range 20 {
					captures = append(captures, liveDiffCapture(fmt.Sprint(i), fmt.Sprintf("file%d.go", i), uint64(i+1), "old\n", "new\n", livediff.Origin{Change: fmt.Sprintf("amber%d", i+1), Caller: "/root", Source: "apply_patch"}))
				}
				c := liveDiffChangesController(t, 120, 28, captures)
				u.shell.diff = c
				c.data = newLiveDiffData()
				c.data.order = []string{"wrong", "target"}
				c.data.attempts["wrong"] = liveDiffAttempt{thread: "child", correlation: "call\x000", change: "amber1", chunks: captures[:1]}
				attempt := liveDiffAttempt{thread: "main", correlation: "call\x000", change: "amber20", chunks: captures[19:]}
				if grouped {
					attempt.correlation = "exec-carrier"
					attempt.receipt = &capturedActivityEdit{thread: "main", calls: []string{"call"}}
				}
				c.data.attempts["target"] = attempt
				path := "earlier.go"
				if repeated {
					path = "file19.go"
				}
				u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "tool", Text: "Edit `" + path + "` +1 -1 · apply_patch", native: &liveActivityNativeItem{thread: "main", item: "earlier"}}}})
				u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 2, Agent: "Main", Kind: "tool", Text: "Edit `file19.go` +1 -1 · apply_patch", native: &liveActivityNativeItem{thread: "main", item: "call"}}}})
				u.shell.side, u.shell.diffOpen = true, true
				c.filterCaller("/root")
				c.navigation.Changes.Query = "amber1"
				c.navigation.ChangesTab = true
				c.openChange("amber1", 0)
				c.refreshChanges()
				if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
					t.Fatal(err)
				}
				clicked := false
				for row, snippet := range u.view.feedSnippets {
					if snippet.block != editNavigationSnippet || snippet.run != 2 {
						continue
					}
					x := u.shell.layout.codex.x + u.view.feedLeft + 1
					y := u.shell.layout.codex.y + u.view.feedTop + row
					for _, ending := range []string{"M", "m"} {
						if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", x, y, ending)); err != nil {
							t.Fatal(err)
						}
					}
					clicked = true
					break
				}
				if !clicked || !u.shell.diffOpen || !u.shell.side || u.shell.focus != 1 || !c.navigation.ChangesTab || c.navigation.Hidden {
					t.Fatal("edit did not open branched navigation")
				}
				if c.navigation.Changes.Target.Change != "amber20" {
					t.Fatalf("wrong change: %+v", c.navigation.Changes.Target)
				}
				if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
					t.Fatal(err)
				}
				l := &c.navigation.Changes
				if l.Cursor < l.Top || l.Cursor >= l.Top+c.navRows || l.Nodes[l.Rows[l.Cursor].Node].Change != "amber20" {
					t.Fatalf("target not visible: cursor=%d top=%d rows=%d", l.Cursor, l.Top, c.navRows)
				}
				// New captures rendered during the preview must remain navigable on return.
				captures = append(captures, liveDiffCapture("later", "later.go", 21, "", "new\n", livediff.Origin{Change: "amber21", Caller: "/root"}))
				c.view.Merge(livediff.GroupCaptures(captures))
				c.view.RefreshVisible()
				if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
					t.Fatal(err)
				}
				// Help closes before the temporary preview returns.
				c.help = true
				if err := u.shell.send("\x1b"); err != nil {
					t.Fatal(err)
				}
				c.escapeKey()
				if c.help || len(u.shell.navigationReturns) != 1 {
					t.Fatal("help consumed preview return")
				}
				if err := u.shell.send("\x1b"); err != nil {
					t.Fatal(err)
				}
				if c.view.Caller != "/root" || c.navigation.Changes.Query != "amber1" || c.navigation.Changes.Target.Change != "amber1" || !u.shell.diffOpen {
					t.Fatal("Edit preview lost prior diff filters or target")
				}
				present := false
				for _, entry := range c.navigation.Entries {
					if entry.Label == "later.go" {
						present = true
					}
				}
				if !present {
					t.Fatal("return restored a stale Files navigator")
				}

			})
		}
	}
}
