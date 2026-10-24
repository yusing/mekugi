package terminalview

// Source: internal/ui/diffview/preview_batch_test.go@[a42ac9f1]
import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

func batchTestPreview(id, caller string, paths ...string) previewmodel.Preview {
	preview := previewmodel.Preview{ID: id, Workspace: "/workspace", Caller: caller, Status: previewmodel.PreviewEdit}
	for _, path := range paths {
		path = "/workspace/" + path
		preview.Files = append(preview.Files, diffmodel.RenderReviewFile(path, path,
			"package main\n\nconst value = 1\n", "package main\n\nconst value = 2\n"))
	}
	return preview
}

func TestNativeBatchSourceScrollAndFollow(t *testing.T) {
	pane := PreviewPane{}
	preview := batchTestPreview("stream", "/root", "src/stream.go")
	preview.Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", "/workspace/src/stream.go", "", strings.Repeat("source line\n", 40))}
	pane.Update(preview, time.Now())
	view := pane.batches["/root"].files[previewFileKey{"stream", "/workspace/src/stream.go"}]
	if view.Source != nil || view.Rendered.ID != "" {
		t.Fatal("queued snapshot parsed source before painting")
	}
	batchTestRender(t, &pane, 16)
	initialFocus := view.Focus
	if !pane.ScrollBatch("/root", 'b') || !view.Paused || view.ScrollRow >= initialFocus {
		t.Fatal("page-up did not pause the file's source viewport")
	}
	pausedAt := view.ScrollRow
	preview.Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", "/workspace/src/stream.go", "", strings.Repeat("source line\n", 60))}
	pane.Update(preview, time.Now())
	batchTestRender(t, &pane, 16)
	if !view.Paused || view.ScrollRow != pausedAt || view.Focus <= initialFocus {
		t.Fatal("streaming update moved a manually paused viewport or failed to advance its live tip")
	}
	if pane.Views["stream"].Paused || pane.ScrollBatch("/root/worker", 'b') || pane.ScrollBatch("/root", '?') {
		t.Fatal("batch scrolling affected the original call or accepted an unrelated caller/key")
	}
	for _, key := range []byte{' ', 'G'} {
		pane.ScrollBatch("/root", 'b')
		for range 10 {
			pane.ScrollBatch("/root", key)
		}
		if view.Paused {
			t.Fatalf("scroll %q to the live tip did not resume following", key)
		}
	}
	pane.Update(batchTestPreview("new", "/root", "src/new.go"), time.Now())
	batchTestRender(t, &pane, 31)
	pane.NextBatch("/root")
	if !pane.ScrollBatch("/root", 'r') || view.Paused || pane.batches["/root"].pinned || pane.batches["/root"].selected.call != "new" {
		t.Fatal("resume did not restore source follow and newest-file selection")
	}
}

func batchTestRender(t *testing.T, pane *PreviewPane, height int) []string {
	t.Helper()
	rows, err := pane.RenderBatch(t.Context(), "/root", "/workspace", diffrender.DarkTheme, 72, height, 15)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestNativeBatchScrollStopsAfterDeletedSuffix(t *testing.T) {
	var pane PreviewPane
	p := batchTestPreview("rewrite", "/root", "source.txt")
	p.Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("/workspace/source.txt", "/workspace/source.txt",
		"retained\n"+strings.Repeat("old baseline\n", 60), strings.Repeat("new source\n", 30)+"retained\n")}
	pane.Update(p, time.Time{})
	batchTestRender(t, &pane, 16)
	view := pane.batches["/root"].files[previewFileKey{"rewrite", "/workspace/source.txt"}]
	if view.Focus >= len(view.Source)-1 {
		t.Fatal("fixture lacks a deleted suffix after the live tip")
	}
	for _, height := range []int{16, 24} {
		batchTestRender(t, &pane, height)
		for range len(view.Source) {
			pane.ScrollBatch("/root", terminalui.PaneWheelDown)
		}
		bottom := batchTestRender(t, &pane, height)
		for range 3 {
			pane.ScrollBatch("/root", terminalui.PaneWheelDown)
			if got := batchTestRender(t, &pane, height); !slices.Equal(got, bottom) {
				t.Fatal("downward scroll moved the rendered bottom viewport")
			}
		}
		if view.ScrollRow != len(view.Source)-1 {
			t.Fatal("deleted suffix did not retain its clamped viewport")
		}
	}
	p.Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("/workspace/source.txt", "/workspace/source.txt",
		"retained\n"+strings.Repeat("old baseline\n", 60), strings.Repeat("new source\n", 30)+"new tip\nretained\n")}
	pane.Update(p, time.Time{})
	if rows := batchTestRender(t, &pane, 24); !strings.Contains(strings.Join(rows, "\n"), "new tip") {
		t.Fatal("deleted-bottom scrolling failed to follow new source")
	}
	for range len(view.Source) {
		pane.ScrollBatch("/root", terminalui.PaneWheelDown)
	}
	pane.ScrollBatch("/root", 'r')
	if view.Paused || view.ScrollRow != 0 {
		t.Fatal("explicit resume did not restore the live tip")
	}
}

func TestNativeBatchContextBottomResumesNewSource(t *testing.T) {
	var pane PreviewPane
	p := batchTestPreview("edit", "/root", "source.txt")
	var source strings.Builder
	for i := range 120 {
		fmt.Fprintf(&source, "line_%03d\n", i)
	}
	before := source.String()
	after := strings.Replace(before, "line_040\n", "new first\n", 1)
	p.Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("/workspace/source.txt", "/workspace/source.txt", before, after)}
	pane.Update(p, time.Time{})
	batchTestRender(t, &pane, 16)
	pane.ScrollBatch("/root", 'b')
	for range 40 {
		pane.ScrollBatch("/root", terminalui.PaneWheelDown)
	}
	view := pane.batches["/root"].files[previewFileKey{"edit", "/workspace/source.txt"}]
	if view.Paused {
		t.Fatal("unchanged context at the bottom prevented automatic follow")
	}
	bottom := batchTestRender(t, &pane, 16)
	for range 3 {
		pane.ScrollBatch("/root", terminalui.PaneWheelDown)
		if got := batchTestRender(t, &pane, 16); !slices.Equal(got, bottom) {
			t.Fatal("following contextual source shook after reaching the bottom")
		}
	}
	after = strings.Replace(after, "line_100\n", "new last\n", 1)
	p.Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("/workspace/source.txt", "/workspace/source.txt", before, after)}
	pane.Update(p, time.Time{})
	if rows := batchTestRender(t, &pane, 16); !strings.Contains(strings.Join(rows, "\n"), "new last") {
		t.Fatal("scrolling to the context bottom failed to follow a later edit")
	}
	// Keep a preexisting addition beyond the next arriving tip. Reaching that
	// physical bottom can enable addition-focused deletion compaction.
	after += "existing added tail\n"
	publish := func(source string) []string {
		p.Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("/workspace/source.txt", "/workspace/source.txt", before, source)}
		pane.Update(p, time.Time{})
		return batchTestRender(t, &pane, 16)
	}
	publish(after)
	after = strings.Replace(after, "new first", "earlier tip", 1)
	publish(after)
	for range len(view.Source) {
		pane.ScrollBatch("/root", terminalui.PaneWheelDown)
	}
	bottom = batchTestRender(t, &pane, 16)
	for range 3 {
		pane.ScrollBatch("/root", terminalui.PaneWheelDown)
		if got := batchTestRender(t, &pane, 16); !slices.Equal(got, bottom) {
			t.Fatal("existing added tail shook after an earlier rewrite")
		}
	}
	if rows := publish(strings.Replace(after, "earlier tip", "latest source", 1)); !strings.Contains(strings.Join(rows, "\n"), "latest source") {
		t.Fatal("added-tail bottom failed to follow newly arriving source")
	}
}

func TestUISnapshotNativeBatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		height int
		setup  func(*testing.T, *PreviewPane)
	}{
		{"one_call_multiple_files", 31, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go"), time.Now())
		}},
		{"capacity", 46, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go", "src/c.go"), time.Now())
		}},
		{"overflow", 31, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go", "src/c.go"), time.Now())
		}},
		{"new_arrival", 31, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go", "src/c.go"), time.Now())
			batchTestRender(t, pane, 31)
			pane.Update(batchTestPreview("second", "/root", "src/d.go"), time.Now())
		}},
		{"pinned_arrival", 31, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go", "src/c.go"), time.Now())
			batchTestRender(t, pane, 31)
			if !pane.NextBatch("/root") {
				t.Fatal("could not cycle batch")
			}
			pane.Update(batchTestPreview("second", "/root", "src/d.go"), time.Now())
		}},
		{"resize_below_minimum", 15, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go"), time.Now())
			batchTestRender(t, pane, 31)
		}},
		{"resize_one_slot", 30, func(t *testing.T, pane *PreviewPane) {
			pane.Update(batchTestPreview("first", "/root", "src/a.go", "src/b.go"), time.Now())
			batchTestRender(t, pane, 31)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pane := PreviewPane{}
			tc.setup(t, &pane)
			assertRowsSnapshot(t, "native_batch_"+tc.name, batchTestRender(t, &pane, tc.height))
		})
	}
}

func TestNativeBatchRetainsCompletedCall(t *testing.T) {
	pane := PreviewPane{}
	first := batchTestPreview("first", "/root", "src/a.go")
	pane.Update(first, time.Now())
	first.Complete = true
	pane.Update(first, time.Now())
	batchTestRender(t, &pane, 31)
	pane.Update(batchTestPreview("second", "/root", "src/b.go"), time.Now())
	if !slices.Equal(pane.Order, []string{"first", "second"}) || !pane.Views["first"].Complete {
		t.Fatalf("completed call replaced: order=%v", pane.Order)
	}
	batchTestRender(t, &pane, 31)
	if len(pane.batches["/root"].order) != 2 {
		t.Fatal("retained call missing from per-file batch")
	}
	// A withdrawal removes only its call and projected file, including retained completions.
	pane.Update(previewmodel.Preview{ID: "first", Workspace: "/workspace"}, time.Now())
	batchTestRender(t, &pane, 31)
	if !slices.Equal(pane.Order, []string{"second"}) || len(pane.batches["/root"].files) != 1 {
		t.Fatal("withdrawal left retained call or removed unrelated call")
	}
}

func TestNativeBatchRetainsSequentialFilesInOneCall(t *testing.T) {
	pane := PreviewPane{}
	pane.Update(batchTestPreview("exec", "/root", "src/a.go"), time.Now())
	batchTestRender(t, &pane, 31)
	firstKey := previewFileKey{"exec", "/workspace/src/a.go"}
	first := pane.batches["/root"].files[firstKey]
	pane.Update(batchTestPreview("exec", "/root", "src/b.go"), time.Now())
	batchTestRender(t, &pane, 31)
	batch := pane.batches["/root"]
	if !slices.Equal(batch.order, []previewFileKey{firstKey, {"exec", "/workspace/src/b.go"}}) || batch.files[firstKey] != first {
		t.Fatal("sequential file projection discarded the earlier file or its viewport")
	}
	completed := pane.Views["exec"].Current
	completed.Complete = true
	pane.Update(completed, time.Now())
	batchTestRender(t, &pane, 31)
	if !first.Complete || !batch.files[previewFileKey{"exec", "/workspace/src/b.go"}].Complete {
		t.Fatal("completion did not settle all projected files in the call")
	}
	pane.Update(previewmodel.Preview{ID: "exec", Workspace: "/workspace"}, time.Now())
	if rows := batchTestRender(t, &pane, 31); len(rows) != 0 || len(batch.files) != 0 || len(batch.order) != 0 {
		t.Fatal("explicit withdrawal retained a sequential file projection")
	}
}

func TestNativeBatchExpiryIsolation(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	hold := 2 * time.Second
	pane := PreviewPane{}
	for _, tc := range []struct {
		id, caller string
		complete   bool
		updated    time.Time
	}{
		{"old", "/root", true, now.Add(-10 * time.Second)},
		{"recent", "/root", true, now.Add(-time.Second)},
		{"worker", "/root/worker", true, now.Add(-10 * time.Second)},
		{"live", "/root", false, now.Add(-10 * time.Second)},
	} {
		preview := batchTestPreview(tc.id, tc.caller, tc.id+".go")
		preview.Complete = tc.complete
		pane.Update(preview, time.Now())
		pane.Views[tc.id].updated = tc.updated
		pane.batch(tc.caller)
	}
	if !pane.ExpireBatches(now, hold) {
		t.Fatal("settled worker batch did not expire")
	}
	if !slices.Equal(pane.Order, []string{"recent", "live"}) || pane.batches["/root/worker"] != nil {
		t.Fatalf("caller isolation failed: order=%v", pane.Order)
	}
	if pane.ExpireBatches(now, hold) {
		t.Fatal("recent update or incomplete call expired")
	}
	if !pane.ExpireBatches(now.Add(time.Second), hold) || !slices.Equal(pane.Order, []string{"live"}) || pane.batches["/root"] == nil {
		t.Fatalf("completed call did not expire at hold boundary: order=%v", pane.Order)
	}
	// Completion starts a fresh hold, even for a call that was live a long time.
	finished := pane.Views["live"].Current
	finished.Complete = true
	pane.Update(finished, time.Now())
	pane.Views["live"].updated = now.Add(time.Second)
	if pane.ExpireBatches(now.Add(2*time.Second), hold) {
		t.Fatal("just-completed call expired early")
	}
	if !pane.ExpireBatches(now.Add(3*time.Second), hold) || len(pane.Views) != 0 || len(pane.batches) != 0 {
		t.Fatal("completed burst or viewport cache survived expiry")
	}
}

func TestUISnapshotNativeBatchCompletedSiblingExpiry(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		name := "follow"
		if pinned {
			name = "pinned"
		}
		t.Run(name, func(t *testing.T) {
			pane := PreviewPane{}
			pane.Update(batchTestPreview("running", "/root", "src/running.go"), time.Now())
			batchTestRender(t, &pane, 16)
			pane.ScrollBatch("/root", 'b')
			finished := batchTestPreview("finished", "/root", "src/done.go", "src/also_done.go")
			finished.Complete = true
			pane.Update(finished, time.Now())
			batchTestRender(t, &pane, 31)
			if pinned {
				pane.NextBatch("/root") // Pin the surviving running file.
			}
			now := pane.Views["finished"].updated.Add(2 * time.Second)
			if !pane.ExpireBatches(now, 2*time.Second) {
				t.Fatal("running sibling retained a completed edit")
			}
			batch := pane.batches["/root"]
			if len(batch.files) != 1 || batch.selected.call != "running" || batch.pinned != pinned {
				t.Fatalf("expired viewports or selection survived: %+v", batch)
			}
			if batch.files[batch.selected].Paused != pinned {
				t.Fatal("expiry failed to resume the newest file or stole the surviving manual selection")
			}
			assertRowsSnapshot(t, "native_batch_expired_sibling_"+name, batchTestRender(t, &pane, 31))
		})
	}
}

func TestUISnapshotNativeBatchToolIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, tool    string
		width, height int
	}{
		{"patch", "apply_patch", 72, 16},
		{"python3", "python3", 72, 16},
		{"cat", "cat", 72, 16},
		{"unknown", "", 72, 16},
		{"short", "apply_patch", 72, 10},
		{"narrow", "python3", 24, 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pane := PreviewPane{}
			preview := batchTestPreview("edit", "/root", "src/a.go")
			preview.Tool = tc.tool
			pane.Update(preview, time.Now())
			rows, err := pane.RenderBatch(t.Context(), "/root", "/workspace", diffrender.DarkTheme, tc.width, tc.height, 15)
			if err != nil {
				t.Fatal(err)
			}
			assertRowsSnapshot(t, "native_batch_tool_"+tc.name, rows)
		})
	}
	pane := PreviewPane{}
	patch := batchTestPreview("patch", "/root", "src/patch.go")
	patch.Tool = "apply_patch"
	pane.Update(patch, time.Now())
	batchTestRender(t, &pane, 16)
	python := batchTestPreview("python", "/root", "src/python.go")
	python.Tool = "python3"
	pane.Update(python, time.Now())
	assertRowsSnapshot(t, "native_batch_tool_follow", batchTestRender(t, &pane, 16))
	pane.NextBatch("/root")
	assertRowsSnapshot(t, "native_batch_tool_pinned", batchTestRender(t, &pane, 16))
	other := batchTestPreview("other-caller", "/root/worker", "src/worker.go")
	other.Tool = "cat"
	pane.Update(other, time.Now())
	// A sibling's tool must not change the pinned caller-local header.
	assertRowsSnapshot(t, "native_batch_tool_pinned", batchTestRender(t, &pane, 16))
}

func TestUISnapshotNativeBatchMixedToolCall(t *testing.T) {
	pane := PreviewPane{}
	preview := batchTestPreview("program", "/root", "src/python.go")
	preview.Tool = "python3"
	pane.Update(preview, time.Now())
	assertRowsSnapshot(t, "native_batch_mixed_tool_initial", batchTestRender(t, &pane, 16))
	// One streaming call can advance to a different editing command and file.
	preview = batchTestPreview("program", "/root", "src/cat.go")
	preview.Tool = "cat"
	pane.Update(preview, time.Now())
	assertRowsSnapshot(t, "native_batch_mixed_tool_follow", batchTestRender(t, &pane, 16))
	if !pane.NextBatch("/root") {
		t.Fatal("could not select the earlier editing command")
	}
	assertRowsSnapshot(t, "native_batch_mixed_tool_pinned", batchTestRender(t, &pane, 16))
}

func TestUISnapshotNativeBatchOmitsDeletedFiles(t *testing.T) {
	pane := PreviewPane{}
	preview := batchTestPreview("mixed", "/root", "src/kept.go")
	preview.Files[0] = diffmodel.RenderReviewFile("", "/workspace/src/kept.go", "", strings.Repeat("source line\n", 24))
	preview.Files = append(preview.Files, diffmodel.RenderReviewFile("/workspace/src/deleted.go", "", "package old\n", ""))
	pane.Update(preview, time.Now())
	assertRowsSnapshot(t, "native_batch_omits_deleted", batchTestRender(t, &pane, 31))
	if !slices.Equal(pane.Callers(), []string{"/root"}) || len(pane.batches["/root"].files) != 1 {
		t.Fatal("deletion reserved a viewport")
	}
}

func TestNativeBatchDeletedFileRemovesRetainedViewport(t *testing.T) {
	pane := PreviewPane{}
	pane.Update(batchTestPreview("call", "/root", "src/a.go", "src/b.go"), time.Now())
	batchTestRender(t, &pane, 31)
	pane.NextBatch("/root") // Pin a.go before it disappears.
	preview := batchTestPreview("call", "/root", "src/b.go")
	preview.Files = append(preview.Files, diffmodel.RenderReviewFile("/workspace/src/a.go", "", "package old\n", ""))
	pane.Update(preview, time.Now())
	batchTestRender(t, &pane, 31)
	b := pane.batches["/root"]
	if len(b.order) != 1 || b.selected.path != "/workspace/src/b.go" || b.pinned {
		t.Fatalf("deleted viewport retained: %+v", b)
	}
	preview.Files = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("/workspace/src/b.go", "", "package old\n", "")}
	pane.Update(preview, time.Now())
	if rows := batchTestRender(t, &pane, 31); len(rows) != 0 || len(pane.Callers()) != 0 {
		t.Fatal("deletion-only call still opens dock")
	}
}
