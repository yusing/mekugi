package router

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/session"
)

func TestUISnapshotNativeRuntimeBatchLifecycle(t *testing.T) {
	for _, mode := range []string{"visible", "passed", "history"} {
		t.Run(mode, func(t *testing.T) {
			u, _ := runtimeTestUI(t)
			for i, path := range []string{"cedar.go", "maple.go"} {
				id := fmt.Sprint("read-", i)
				u.runtimeEntry(session.Event{Kind: "tool", ID: id, Role: "Read", Text: fmt.Sprintf(`{"file_path":%q}`, path), Historical: mode == "history"})
				u.runtimeEntry(session.Event{Kind: "tool_result", ID: id, Text: "package fixture", Historical: mode == "history"})
			}
			v := u.view
			feed := v.renderFeed(60, 8)
			if mode == "passed" {
				for i := range 12 {
					u.runtimeEntry(session.Event{Kind: "message", ID: fmt.Sprint("note-", i), Text: fmt.Sprint("Later note ", i)})
				}
				feed = v.renderFeed(60, 8)
				v.following, v.offset = false, len(feed.lines)-8
				v.viewport(feed, 8)
				if !v.passed[v.entries[0].Seq] {
					t.Fatal("native batch did not leave the viewport")
				}
				feed = v.renderFeed(60, 8)
			}
			// Snapshot the batch itself; later notes only establish real viewport passage.
			rows := feed.lines
			if mode == "passed" {
				rows = rows[:1]
			}
			assertNativeUISnapshot(t, "native-runtime-batch-"+mode, rows)
			if mode != "visible" {
				row := slices.IndexFunc(feed.lines, func(row string) bool { return strings.Contains(row, "2 files") })
				if row < 0 || !u.shell.openOutput(v, feed.snippets[row]) || len(u.shell.output.pages) != 2 {
					t.Fatal("native collapsed batch lost its shared output target")
				}
			}
		})
	}
}
