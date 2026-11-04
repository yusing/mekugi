package executionobservation

import (
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestRetireJoinsCanceledPreviewBeforeResourceCleanup(t *testing.T) {
	workspace, _, observation := captureRunningExecScope(t)
	synctest.Test(t, func(t *testing.T) {
		reading, release := make(chan struct{}), make(chan struct{})
		review := sessionreview.New(t.Context(), previewmodel.New(func(path string, remaining *int) changecapture.File {
			close(reading)
			<-release
			return changecapture.ReadFile(path, remaining)
		}), nil)
		defer review.Close()
		broker := review.Preview()
		broker.Enable()
		review.IncludeThread(workspace, "child", true)
		parent := new(Windows)
		child := parent.ForRuntime()
		child.open(&execWindow{ref: "preview", thread: "child", turn: "turn", roots: []string{workspace}})
		child.preview("preview", *observation, broker, workspace, "child", "/root/child", nil)
		<-reading
		// A sibling runtime must not join this worker. Turn cancellation must
		// remain nonblocking even while an endpoint read is still in flight.
		parent.Retire()
		child.StopTurnPreviews("child", "turn")
		retired := make(chan struct{})
		go func() {
			child.Retire()
			close(retired)
		}()
		synctest.Wait()
		select {
		case <-retired:
			t.Error("resource cleanup could race the canceled preview worker")
		default:
		}
		close(release)
		<-retired
	})
}

func TestStopTurnPreviewsPreservesCapture(t *testing.T) {
	windows := new(Windows)
	canceled := false
	otherCanceled := false
	windows.open(&execWindow{ref: "main", thread: "main", turn: "turn", previewCancel: func() { canceled = true }})
	windows.open(&execWindow{ref: "child", thread: "child", turn: "turn", previewCancel: func() { otherCanceled = true }})
	windows.StopTurnPreviews("main", "turn")
	if !canceled || otherCanceled || !windows.find("main").closed.IsZero() || !windows.find("child").closed.IsZero() {
		t.Fatal("preview stop crossed owners or finalized capture")
	}
}

func TestRetireRuntimePreservesOverlappingSiblings(t *testing.T) {
	workspace := t.TempDir()
	parent := &Windows{}
	child := parent.ForRuntime()
	canceled := false
	parent.open(&execWindow{ref: "parent", roots: []string{workspace}})
	child.open(&execWindow{ref: "child", roots: []string{workspace}, paths: []string{filepath.Join(workspace, "child.txt")}, previewCancel: func() { canceled = true }})
	child.Retire()
	if !canceled || child.find("child").closed.IsZero() || !parent.find("parent").closed.IsZero() {
		t.Fatal("runtime retirement missed its window or closed its sibling")
	}
	if view := parent.close("parent"); !slices.Equal(view.overlaps, []string{"child"}) || len(view.excluded) != 1 {
		t.Fatalf("retirement lost an already overlapping writer: %+v", view)
	}
	resumed := parent.ForRuntime()
	resumed.open(&execWindow{ref: "resumed", roots: []string{workspace}})
	if view := resumed.close("resumed"); len(view.overlaps) != 0 || len(view.excluded) != 0 {
		t.Fatalf("retired runtime excluded later writes: %+v", view)
	}
}
