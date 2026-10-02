package diffview

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
)

func batchTestPreview(id, caller string, paths ...string) Preview {
	preview := Preview{ID: id, Workspace: "/workspace", Caller: caller, Status: PreviewEdit}
	for _, path := range paths {
		path = "/workspace/" + path
		preview.Files = append(preview.Files, mekugi.RenderReviewFile(path, path,
			"package main\n\nconst value = 1\n", "package main\n\nconst value = 2\n"))
	}
	return preview
}

func TestNativeBatchSourceScrollAndFollow(t *testing.T) {
	pane := PreviewPane{Retain: true}
	preview := batchTestPreview("stream", "/root", "src/stream.go")
	preview.Files = []mekugi.ReviewFile{mekugi.RenderReviewFile("", "/workspace/src/stream.go", "", strings.Repeat("source line\n", 40))}
	pane.Update(preview)
	batchTestRender(t, &pane, 16)
	view := pane.batches["/root"].files[previewFileKey{"stream", "/workspace/src/stream.go"}]
	initialFocus := view.Focus
	if !pane.ScrollBatch("/root", 'b') || !view.Paused || view.ScrollRow >= initialFocus {
		t.Fatal("page-up did not pause the file's source viewport")
	}
	pausedAt := view.ScrollRow
	preview.Files = []mekugi.ReviewFile{mekugi.RenderReviewFile("", "/workspace/src/stream.go", "", strings.Repeat("source line\n", 60))}
	pane.Update(preview)
	batchTestRender(t, &pane, 16)
	if !view.Paused || view.ScrollRow != pausedAt || view.Focus <= initialFocus {
		t.Fatal("streaming update moved a manually paused viewport or failed to advance its live tip")
	}
	if pane.Views["stream"].Paused || pane.ScrollBatch("/root/worker", 'b') || pane.ScrollBatch("/root", '?') {
		t.Fatal("batch scrolling affected the original call or accepted an unrelated caller/key")
	}
	pane.Update(batchTestPreview("new", "/root", "src/new.go"))
	batchTestRender(t, &pane, 31)
	pane.NextBatch("/root")
	if !pane.ScrollBatch("/root", 'r') || view.Paused || pane.batches["/root"].pinned || pane.batches["/root"].selected.call != "new" {
		t.Fatal("resume did not restore source follow and newest-file selection")
	}
}

func batchTestRender(t *testing.T, pane *PreviewPane, height int) []string {
	t.Helper()
	rows, err := pane.RenderBatch(t.Context(), "/root", "/workspace", livediff.DarkTheme, 72, height, 15)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestUISnapshotNativeBatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		height int
		setup  func(*testing.T, *PreviewPane)
	}{
		{"one_call_multiple_files", 31, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go"))
		}},
		{"capacity", 46, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go", "src/c.go"))
		}},
		{"overflow", 31, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go", "src/c.go"))
		}},
		{"new_arrival", 31, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go", "src/c.go"))
			batchTestRender(t, pane, 31)
			pane.Update(batchTestPreview("second", "/root", "src/d.go"))
		}},
		{"pinned_arrival", 31, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go", "src/c.go"))
			batchTestRender(t, pane, 31)
			if !pane.NextBatch("/root") {
				t.Fatal("could not cycle batch")
			}
			pane.Update(batchTestPreview("second", "/root", "src/d.go"))
		}},
		{"resize_below_minimum", 15, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go"))
			batchTestRender(t, pane, 31)
		}},
		{"resize_one_slot", 30, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go"))
			batchTestRender(t, pane, 31)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pane := PreviewPane{Retain: true}
			tc.setup(t, &pane)
			assertRowsSnapshot(t, "native_batch_"+tc.name, batchTestRender(t, &pane, tc.height))
		})
	}
}

func TestNativeBatchRetainsCompletedCall(t *testing.T) {
	pane := PreviewPane{Retain: true}
	first := batchTestPreview("first", "/root", "src/a.go")
	pane.Update(first)
	first.Complete = true
	pane.Update(first)
	batchTestRender(t, &pane, 31)
	pane.Update(batchTestPreview("second", "/root", "src/b.go"))
	if !slices.Equal(pane.Order, []string{"first", "second"}) || !pane.Views["first"].Complete {
		t.Fatalf("completed call replaced: order=%v", pane.Order)
	}
	batchTestRender(t, &pane, 31)
	if len(pane.batches["/root"].order) != 2 {
		t.Fatal("retained call missing from per-file batch")
	}
	// A withdrawal removes only its call and projected file, including retained completions.
	pane.Update(Preview{ID: "first", Workspace: "/workspace"})
	batchTestRender(t, &pane, 31)
	if !slices.Equal(pane.Order, []string{"second"}) || len(pane.batches["/root"].files) != 1 {
		t.Fatal("withdrawal left retained call or removed unrelated call")
	}
}

func TestNativeBatchRetainsSequentialFilesInOneCall(t *testing.T) {
	pane := PreviewPane{Retain: true}
	pane.Update(batchTestPreview("code-mode", "/root", "src/a.go"))
	batchTestRender(t, &pane, 31)
	firstKey := previewFileKey{"code-mode", "/workspace/src/a.go"}
	first := pane.batches["/root"].files[firstKey]
	pane.Update(batchTestPreview("code-mode", "/root", "src/b.go"))
	batchTestRender(t, &pane, 31)
	batch := pane.batches["/root"]
	if !slices.Equal(batch.order, []previewFileKey{firstKey, {"code-mode", "/workspace/src/b.go"}}) || batch.files[firstKey] != first {
		t.Fatal("sequential file projection discarded the earlier file or its viewport")
	}
	completed := pane.Views["code-mode"].Current
	completed.Complete = true
	pane.Update(completed)
	batchTestRender(t, &pane, 31)
	if !first.Complete || !batch.files[previewFileKey{"code-mode", "/workspace/src/b.go"}].Complete {
		t.Fatal("completion did not settle all projected files in the call")
	}
	pane.Update(Preview{ID: "code-mode", Workspace: "/workspace"})
	if rows := batchTestRender(t, &pane, 31); len(rows) != 0 || len(batch.files) != 0 || len(batch.order) != 0 {
		t.Fatal("explicit withdrawal retained a sequential file projection")
	}
}

func TestNativeBatchExpiryIsolation(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	hold := 2 * time.Second
	pane := PreviewPane{Retain: true}
	for _, tc := range []struct {
		id, caller string
		complete   bool
		updated    time.Time
	}{
		{"old", "/root", true, now.Add(-10 * time.Second)},
		{"recent", "/root", true, now.Add(-time.Second)},
		{"worker", "/root/worker", true, now.Add(-10 * time.Second)},
		{"live", "/root/live", false, now.Add(-10 * time.Second)},
	} {
		preview := batchTestPreview(tc.id, tc.caller, tc.id+".go")
		preview.Complete = tc.complete
		pane.Update(preview)
		pane.Views[tc.id].updated = tc.updated
		pane.batch(tc.caller)
	}
	if !pane.ExpireBatches(now, hold) {
		t.Fatal("settled worker batch did not expire")
	}
	if !slices.Equal(pane.Order, []string{"old", "recent", "live"}) || pane.batches["/root/worker"] != nil {
		t.Fatalf("caller isolation failed: order=%v", pane.Order)
	}
	if pane.ExpireBatches(now, hold) {
		t.Fatal("recent update or incomplete call expired")
	}
	if !pane.ExpireBatches(now.Add(time.Second), hold) || !slices.Equal(pane.Order, []string{"live"}) || pane.batches["/root"] != nil {
		t.Fatalf("entire burst did not expire at hold boundary: order=%v", pane.Order)
	}
	// Completion starts a fresh hold, even for a call that was live a long time.
	finished := pane.Views["live"].Current
	finished.Complete = true
	pane.Update(finished)
	pane.Views["live"].updated = now.Add(time.Second)
	if pane.ExpireBatches(now.Add(2*time.Second), hold) {
		t.Fatal("just-completed call expired early")
	}
	if !pane.ExpireBatches(now.Add(3*time.Second), hold) || len(pane.Views) != 0 || len(pane.batches) != 0 {
		t.Fatal("completed burst or viewport cache survived expiry")
	}
}

func TestUISnapshotNativeBatchToolIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, tool    string
		width, height int
	}{
		{"patch", "apply_patch", 72, 16},
		{"shell", "exec_command", 72, 16},
		{"code_mode", "exec", 72, 16},
		{"unknown", "", 72, 16},
		{"short", "apply_patch", 72, 10},
		{"narrow", "exec_command", 24, 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pane := PreviewPane{Retain: true}
			preview := batchTestPreview("edit", "/root", "src/a.go")
			preview.Tool = tc.tool
			pane.Update(preview)
			rows, err := pane.RenderBatch(t.Context(), "/root", "/workspace", livediff.DarkTheme, tc.width, tc.height, 15)
			if err != nil {
				t.Fatal(err)
			}
			assertRowsSnapshot(t, "native_batch_tool_"+tc.name, rows)
		})
	}
	pane := PreviewPane{Retain: true}
	patch := batchTestPreview("patch", "/root", "src/patch.go")
	patch.Tool = "apply_patch"
	pane.Update(patch)
	batchTestRender(t, &pane, 16)
	shell := batchTestPreview("shell", "/root", "src/shell.go")
	shell.Tool = "exec_command"
	pane.Update(shell)
	assertRowsSnapshot(t, "native_batch_tool_follow", batchTestRender(t, &pane, 16))
	pane.NextBatch("/root")
	assertRowsSnapshot(t, "native_batch_tool_pinned", batchTestRender(t, &pane, 16))
	other := batchTestPreview("other-caller", "/root/worker", "src/worker.go")
	other.Tool = "exec"
	pane.Update(other)
	// A sibling's tool must not change the pinned caller-local header.
	assertRowsSnapshot(t, "native_batch_tool_pinned", batchTestRender(t, &pane, 16))
}
