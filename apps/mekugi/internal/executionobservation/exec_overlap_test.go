package executionobservation

import (
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestExecWindowSiblingMergeAndOtherOverlap(t *testing.T) {
	root := t.TempDir()
	r := &Windows{}
	aPath := filepath.Join(root, "a")
	bPath := filepath.Join(root, "b")
	r.open(&execWindow{ref: "a", roots: []string{root}, paths: []string{aPath}, group: "same"})
	r.open(&execWindow{ref: "b", roots: []string{root}, paths: []string{bPath}, group: "same"})
	if view := r.close("a", "b"); len(view.overlaps) != 0 || len(view.excluded) != 0 {
		t.Fatalf("merged siblings should not overlap or exclude each other: %+v", view)
	}

	r = &Windows{}
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
	r := &Windows{}
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

func TestExecWindowRuntimeFamily(t *testing.T) {
	root := t.TempDir()
	parent := &Windows{}
	child := parent.ForRuntime()
	runtimes := []*Windows{parent, child, parent.ForRuntime(), child.ForRuntime()}
	names := []string{"parent", "child", "sibling", "grandchild"}
	for i, runtime := range runtimes {
		runtime.open(&execWindow{ref: names[i], thread: names[i], roots: []string{root}, paths: []string{filepath.Join(root, names[i])}})
	}
	independent := &Windows{}
	independent.open(&execWindow{ref: "independent", roots: []string{root}})
	if view := independent.close("independent"); len(view.overlaps) != 0 {
		t.Fatalf("independent family shared windows: %+v", view)
	}
	for i, runtime := range runtimes {
		view := runtime.close(names[i])
		if len(view.overlaps) != len(runtimes)-1 || len(view.excluded) != len(runtimes)-1 || slices.Contains(view.overlaps, names[i]) {
			t.Fatalf("runtime %s lost family overlap: %+v", names[i], view)
		}
	}
}

func TestExecFinalizationOverlappingScopeLocks(t *testing.T) {
	r := &Windows{}
	release := sync.OnceFunc(r.serialize("/held-workspace", "/held-scope"))
	defer release()
	disjoint := make(chan struct{})
	go func() {
		r.serialize("/other-workspace", "/other-scope")()
		close(disjoint)
	}()
	select {
	case <-disjoint:
	case <-time.After(5 * time.Second):
		t.Fatal("unrelated workspace scopes shared a comparison lock")
	}
	release()
	// A bridge covers both independently held scopes; parent/subdirectory
	// overlap and repeated workspaces exercise multi-lock ordering as well.
	workspaces := []string{"/workspace-a", "/workspace-b", "/workspace-c", "/workspace-d"}
	roots := [][]string{{"/shared/a"}, {"/shared/b"}, {"/shared/a/sub", "/shared/b/sub"}, {"/separate"}}
	var workers sync.WaitGroup
	var mu sync.Mutex
	active := make(map[int][]string)
	start, done := make(chan struct{}), make(chan struct{})
	for i := range 32 {
		workers.Go(func() {
			<-start
			scope := i % len(roots)
			release := r.serialize(workspaces[scope], roots[scope]...)
			mu.Lock()
			for _, other := range active {
				if execRootsMeet(other, roots[scope]) {
					t.Error("overlapping physical scopes entered comparison together")
				}
			}
			active[i] = roots[scope]
			mu.Unlock()
			runtime.Gosched()
			mu.Lock()
			delete(active, i)
			mu.Unlock()
			release()
		})
	}
	close(start)
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("overlapping scope comparisons deadlocked")
	}
	if len(r.state().finalizing) != 0 {
		t.Fatal("completed comparisons retained scope locks")
	}
}
