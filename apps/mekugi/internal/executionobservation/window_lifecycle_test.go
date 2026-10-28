package executionobservation

import (
	"path/filepath"
	"slices"
	"testing"
)

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
