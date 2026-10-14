package router

import (
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestTerminalViewMemory(t *testing.T) {
	u, _ := workflowUI(t)
	child := u.orchestrateThreads["child"]
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	// Bound the fixture, not the product: restored roots use the same consumer
	// as live implementations and reviewers, but do not need a painted Diff.
	for i := range 8 {
		thread := fmt.Sprintf("retained-review-%d", i)
		d := orchestrate.WorkflowDispatch{ID: thread, ThreadID: thread, Kind: orchestrate.WorkflowDispatchKind(orchestrate.ReviewCorrectness), HostStatus: "completed"}
		raw := mustMarshalJSON(map[string]any{"thread": appServerThreadInfo{ID: thread, Cwd: child.batch.Cwd}})
		if err := u.addWorkflowReviewView(child, d, raw, true, func() {}); err != nil {
			t.Fatal(err)
		}
	}
	drainOrchestrateWork(t, u)
	runtime.GC()
	runtime.ReadMemStats(&after)
	retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("8 restored views: allocated=%d retained=%d bytes", after.TotalAlloc-before.TotalAlloc, retained)
	if retained > 8<<20 {
		t.Errorf("unpainted views retained %d bytes; each view must not retain a parser buffer", retained)
	}
	// Use the actual native Diff renderer, visit every retained root, then
	// resize between roots, and return to Main. Saved Diff and styles survive.
	u.shell.diffOpen, u.shell.journalOpen = true, false
	var first string
	for pass := range 2 {
		for _, thread := range u.navigation.order {
			v := u.navigation.views[thread]
			v.modelsLoading = true
			if !u.switchOrchestratedThread(thread) {
				t.Fatal("view is not selectable", thread)
			}
			if pass == 0 {
				v.shell.diff.view.Merge(livediff.GroupCaptures([]livediff.Chunk{liveDiffCapture(thread, thread+".go", 1, "old\n", "new é\n", livediff.Origin{})}))
				v.shell.diff.view.RefreshVisible()
			}
			width := 120
			if pass == 1 && thread != "main" {
				width = 100
			}
			if err := v.paint(io.Discard, width, 32); err != nil {
				t.Fatal(err)
			}
			frame := v.shell.diffScreen.Render()
			plain := ansi.Strip(frame)
			if !strings.Contains(plain, "new é") || !strings.Contains(plain, thread+".go") {
				t.Fatal("saved Diff did not render", thread, frame)
			}
			if thread == "main" {
				if pass == 0 {
					first = frame
				} else if frame != first {
					t.Fatal("returning to Main changed terminal cells or styles")
				}
			}
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	retained = int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("10 visited views: retained=%d bytes", retained)
	if retained > 12<<20 {
		t.Errorf("visited views retained %d bytes; hidden views must not retain parser buffers", retained)
	}
	runtime.KeepAlive(u)
}
