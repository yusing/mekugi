package sessionreview

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/changehistory/changes"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type blockedChanges struct {
	changes.Service
	entered, release chan struct{}
	once             sync.Once
}

func (s *blockedChanges) InspectVisible(changes.Scope, string, map[string]bool) ([]changes.Snapshot, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return nil, nil
}

func blockedProjection(t *testing.T) (Projection, *blockedChanges, func()) {
	t.Helper()
	service, err := history.NewService(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := service.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blocked := &blockedChanges{entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(blocked.release) })
	t.Cleanup(release)
	projection := Projection{Changes: blocked, Retained: changes.Scope{Store: store}}
	projection.Reset()
	return projection, blocked, release
}

func waitBlocked(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("retained read did not reach storage")
	}
}

// Source: apps/mekugi/internal/router/saved_review_loader_test.go@bab31cbe.
func TestSavedReviewLoaderPhysicalCapacitySurvivesReplacement(t *testing.T) {
	state, first, releaseFirst := blockedProjection(t)
	loader := NewLoader(t.Context(), state)
	t.Cleanup(loader.Close)
	event := Event{Kind: "scope", Resync: true, Scope: &previewmodel.Scope{Workspaces: map[string]map[string]bool{"/workspace": {"child": true}}}}
	if err := loader.Enqueue(event); err != nil {
		t.Fatal(err)
	}
	waitBlocked(t, first.entered)
	state, second, releaseSecond := blockedProjection(t)
	loader.Replace(t.Context(), state)
	if err := loader.Enqueue(event); err != nil {
		t.Fatal(err)
	}
	waitBlocked(t, second.entered)
	loader.Replace(t.Context(), state)
	if err := loader.Enqueue(event); err == nil || !strings.Contains(err.Error(), "capacity exhausted") {
		t.Fatalf("third physical read was not bounded: %v", err)
	}
	releaseFirst()
	releaseSecond()
	select {
	case loader.slots <- struct{}{}:
		<-loader.slots
	case <-time.After(5 * time.Second):
		t.Fatal("retired storage worker did not release capacity")
	}
	if err := loader.Enqueue(event); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case result := <-loader.Updates():
			accepted, err := loader.Accept(result)
			if err != nil {
				t.Fatal(err)
			}
			if accepted {
				if !loader.Projection().Scope.Workspaces["/workspace"]["child"] {
					t.Fatal("released capacity did not publish the current scope")
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("replacement read did not finish")
		}
	}
}

func TestLoaderBacklogOverflowDropsQueuedChanges(t *testing.T) {
	state, blocked, release := blockedProjection(t)
	loader := NewLoader(t.Context(), state)
	t.Cleanup(loader.Close)
	event := Event{Kind: "scope", Resync: true, Scope: &previewmodel.Scope{Workspaces: map[string]map[string]bool{"/workspace": {"child": true}}}}
	if err := loader.Enqueue(event); err != nil {
		t.Fatal(err)
	}
	waitBlocked(t, blocked.entered)
	for range 34 {
		if err := loader.Enqueue(Event{Kind: "change", Changes: []Change{{Workspace: "/workspace", Thread: "child"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if !loader.NeedsResync() || len(loader.pending) != 0 {
		t.Fatal("overflow retained a durable backlog instead of requesting resync")
	}
	release()
}
