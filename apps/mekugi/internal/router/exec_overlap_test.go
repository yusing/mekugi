package router

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestExecWindowSiblingMergeAndOtherOverlap(t *testing.T) {
	root := t.TempDir()
	r := &execWindowRegistry{}
	aPath := filepath.Join(root, "a")
	bPath := filepath.Join(root, "b")
	r.open(&execWindow{ref: "a", roots: []string{root}, paths: []string{aPath}, group: "same"})
	r.open(&execWindow{ref: "b", roots: []string{root}, paths: []string{bPath}, group: "same"})
	if view := r.close("a", "b"); len(view.overlaps) != 0 || len(view.excluded) != 0 {
		t.Fatalf("merged siblings should not overlap or exclude each other: %+v", view)
	}

	r = &execWindowRegistry{}
	r.open(&execWindow{ref: "first", roots: []string{root}, paths: []string{aPath}})
	r.open(&execWindow{ref: "child", roots: []string{filepath.Join(root, "sub")}, paths: []string{bPath}})
	view := r.close("first")
	if !slices.Equal(view.overlaps, []string{"child"}) || !slices.Equal(view.excluded, []string{bPath}) {
		t.Fatalf("nested-root overlap should exclude other call scope: %+v", view)
	}
	view = r.close("child")
	if !slices.Equal(view.overlaps, []string{"first"}) || !slices.Equal(view.excluded, []string{aPath}) {
		t.Fatalf("other side of overlap should retain first call scope: %+v", view)
	}
}

func TestExecWindowDisjointRootsAndBackground(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	r := &execWindowRegistry{}
	r.open(&execWindow{ref: "one", roots: []string{root}, thread: "thread", turn: "old", session: "session:42"})
	r.open(&execWindow{ref: "elsewhere", roots: []string{other}, thread: "other"})
	if view := r.close("elsewhere"); len(view.overlaps) != 0 || len(view.background) != 0 {
		t.Fatalf("disjoint roots should not overlap: %+v", view)
	}
	r.markBackground("thread", "new")
	r.open(&execWindow{ref: "later", roots: []string{root}, thread: "thread", turn: "new"})
	view := r.close("later")
	if len(view.overlaps) != 0 || !slices.Equal(view.background, []string{"background session 42"}) {
		t.Fatalf("running old-turn window should be a background label, not overlap: %+v", view)
	}
}
