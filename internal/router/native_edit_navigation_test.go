package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
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

func TestNativeCompletedBatchEditOpensBeforeSiblingCommandExits(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprint(child), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			thread, view := "main", u.view
			if child {
				thread, view = "child", u.agents
				u.session.registerThread(appServerThreadInfo{ID: thread, AgentNickname: "worker"})
				u.session.metadata[thread] = ""
				u.shell.selectNativePane(2)
			}
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "batch", "status": "inProgress"}})
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "batch", "item": map[string]any{"id": "slow-sibling", "type": "commandExecution", "command": "sleep 30", "status": "inProgress"}})
			change := map[string]any{"path": "batch.go", "kind": map[string]any{"type": "update"}, "diff": "@@ -1 +1 @@\n-before_batch\n+after_batch\n"}
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "batch", "item": map[string]any{"id": "patch", "type": "fileChange", "status": "completed", "changes": []any{change}}})
			finishPacing(u.view, u.agents)
			screen := vt.NewEmulator(160, 40)
			defer screen.Close()
			if err := u.paint(screen, 160, 40); err != nil {
				t.Fatal(err)
			}
			if len(u.shell.diff.data.order) != 0 || u.session.commands[[3]string{thread, "batch", "slow-sibling"}] == nil {
				t.Fatal("fixture must have no durable capture and a running batch sibling")
			}
			// Click the actual rendered edit, through the shared pointer routing.
			x, y := -1, -1
			for row := range 40 {
				for col := range 160 - len("batch.go") {
					var text strings.Builder
					for offset := range len("batch.go") {
						text.WriteString(screen.CellAt(col+offset, row).Content)
					}
					if text.String() == "batch.go" {
						x, y = col, row
						break
					}
				}
				if x >= 0 {
					break
				}
			}
			if x < 0 {
				t.Fatalf("edit row missing:\n%s", screen.String())
			}
			for _, end := range []string{"M", "m"} {
				if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", x+1, y+1, end)); err != nil {
					t.Fatal(err)
				}
			}
			if u.shell.output == nil {
				t.Fatal("completed nested edit was unresponsive before batch exit")
			}
			if err := u.paint(screen, 160, 40); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(u.shell.output.laid.Text, "+after_batch") || u.shell.output.view != view {
				t.Fatalf("wrong edit opened:\n%s", screen.String())
			}
			if u.session.commands[[3]string{thread, "batch", "slow-sibling"}] == nil {
				t.Fatal("opening a diff altered host command lifecycle")
			}
		})
	}
}

func TestNativeHostEditNavigationRequiresCompletedSuccessfulItem(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	for _, tc := range []struct{ phase, status string }{{"item/started", ""}, {"item/completed", "failed"}, {"item/completed", "declined"}, {"item/completed", "inProgress"}} {
		item := appServerItem{Type: "fileChange", Status: tc.status, Changes: []appServerFileChange{{Path: "a.go", Diff: "+not_confirmed\n"}}}
		u.view.entries = []liveActivityRecord{{Seq: 1, native: &liveActivityNativeItem{thread: "main", item: "patch", editPages: appServerEditPages(item, u.session.cwd, tc.phase)}}}
		if u.shell.openActivityEdit(u.view, 1, "a.go") || u.shell.output != nil {
			t.Fatalf("%s/%s opened a completed edit", tc.phase, tc.status)
		}
	}
	item := appServerItem{Changes: []appServerFileChange{{Path: "first.go", Diff: "+first\n"}, {Path: "second.go", Diff: "+second\n"}}}
	u.view.entries = []liveActivityRecord{{Seq: 2, native: &liveActivityNativeItem{thread: "main", item: "patch", editPages: appServerEditPages(item, u.session.cwd, "item/completed")}}}
	if u.shell.openActivityEdit(u.view, 2, "missing.go") || !u.shell.openActivityEdit(u.view, 2, "second.go") || u.shell.output.page != 1 {
		t.Fatal("host diff did not resolve the exact clicked path")
	}
}

func TestUISnapshotNativeCompletedHostEditDialog(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	item := appServerItem{Changes: []appServerFileChange{{Path: "internal/router/batch.go", Diff: "@@ -1 +1 @@\n-before_batch\n+after_batch\n"}}}
	u.view.entries = []liveActivityRecord{{Seq: 1, native: &liveActivityNativeItem{thread: "main", item: "patch", editPages: appServerEditPages(item, u.session.cwd, "item/completed")}}}
	if !u.shell.openActivityEdit(u.view, 1, "internal/router/batch.go") {
		t.Fatal("host edit did not open")
	}
	u.view.painter.Theme = livediff.DarkTheme
	uisnapshot.Assert(t, "testdata/snapshots/native-completed-host-edit-dialog.txt", drawOutputDialog(u.shell)+"\n")
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
