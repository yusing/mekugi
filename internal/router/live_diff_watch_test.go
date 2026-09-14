package router

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func waitLiveDiffEvent(t *testing.T, watcher *liveDiffWatcher, path string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, open := <-watcher.Events:
			if !open {
				t.Fatal("watcher closed before publication")
			}
			if event.Name == path && watcher.relevant(event) {
				return
			}
		case err := <-watcher.Errors:
			t.Fatal(err)
		case <-timer.C:
			t.Fatalf("no invalidation for %s", path)
		}
	}
}

func TestLiveDiffWatchAtomicPublication(t *testing.T) {
	directory, workspace := t.TempDir(), t.TempDir()
	scope := filepath.Join(t.TempDir(), "session.json")
	writeLiveDiffScope(t, scope, map[string]map[string]bool{workspace: {"thread": true}})
	watcher, err := newLiveDiffWatcher(directory, workspace, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	path := filepath.Join(directory, changeIndexName(workspace))
	for range 3 {
		pending := filepath.Join(directory, "changes-pending-test")
		if err := os.WriteFile(pending, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(pending, path); err != nil {
			t.Fatal(err)
		}
		waitLiveDiffEvent(t, watcher, path)
	}
	// Receipt replacements use exactly the same atomic publication boundary.
	second := t.TempDir()
	writeLiveDiffScope(t, scope, map[string]map[string]bool{second: {"child": true}})
	waitLiveDiffEvent(t, watcher, scope)
	if err := watcher.sync(map[string]changeIndex{second: {}}); err != nil {
		t.Fatal(err)
	}
	if watcher.relevant(fsnotify.Event{Name: path, Op: fsnotify.Create}) {
		t.Fatal("old workspace still invalidates the scoped view")
	}
	if !watcher.relevant(fsnotify.Event{Name: filepath.Join(directory, changeIndexName(second)), Op: fsnotify.Create}) {
		t.Fatal("new workspace does not invalidate the scoped view")
	}
	if watcher.relevant(fsnotify.Event{Name: filepath.Join(directory, "call-pending-test"), Op: fsnotify.Write}) {
		t.Fatal("unpublished replay record invalidates the view")
	}
	if err := os.Remove(scope); err != nil {
		t.Fatal(err)
	}
	waitLiveDiffEvent(t, watcher, scope)
}

func TestLiveDiffWatchMissingStore(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	directory := filepath.Join(root, "missing", "replay")
	watcher, err := newLiveDiffWatcher(directory, workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	waitLiveDiffEvent(t, watcher, filepath.Join(root, "missing"))
	if err := watcher.sync(map[string]changeIndex{workspace: {}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, changeIndexName(workspace))
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	waitLiveDiffEvent(t, watcher, path)
}

func TestLiveDiffWatchInvalidDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, nil, 0600); err != nil {
		t.Fatal(err)
	}
	watcher, err := newLiveDiffWatcher(filepath.Join(parent, "replay"), t.TempDir(), "")
	if err == nil {
		watcher.Close()
		t.Fatal("invalid watch directory accepted")
	}
}
