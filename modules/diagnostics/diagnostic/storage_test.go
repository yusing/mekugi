package diagnostic

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistenceDebugRetentionRespectsOwnershipAndLease(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	old := now.Add(-14*24*time.Hour - time.Second)
	makeBundle := func() (string, func() error) {
		t.Helper()
		directory, release, err := createBundle(root)
		if err != nil {
			t.Fatal(err)
		}
		return directory, release
	}
	age := func(directory string, stamp time.Time) {
		t.Helper()
		if err := os.Chtimes(filepath.Join(directory, debugBundleLease), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	aged, release := makeBundle()
	if err := release(); err != nil {
		t.Fatal(err)
	}
	age(aged, old)
	custom := filepath.Join(t.TempDir(), "capture.jsonl")
	if err := os.WriteFile(custom, []byte("keep capture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(custom, filepath.Join(aged, "capture.jsonl")); err != nil {
		t.Fatal(err)
	}
	active, releaseActive := makeBundle()
	defer func() {
		if err := releaseActive(); err != nil {
			t.Error(err)
		}
	}()
	age(active, old)
	fresh, releaseFresh := makeBundle()
	if err := releaseFresh(); err != nil {
		t.Fatal(err)
	}
	boundary, releaseBoundary := makeBundle()
	if err := releaseBoundary(); err != nil {
		t.Fatal(err)
	}
	age(boundary, now.Add(-14*24*time.Hour))
	unmarked := filepath.Join(root, "mekugi-debug-unmarked")
	if err := os.Mkdir(unmarked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unmarked, old, old); err != nil {
		t.Fatal(err)
	}
	linkedMarker := filepath.Join(root, "mekugi-debug-linked-marker")
	if err := os.Mkdir(linkedMarker, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(custom, filepath.Join(linkedMarker, debugBundleLease)); err != nil {
		t.Fatal(err)
	}
	linkedBundle := filepath.Join(root, "mekugi-debug-linked-bundle")
	if err := os.Symlink(active, linkedBundle); err != nil {
		t.Fatal(err)
	}
	if err := new(Service).CleanupBundles(t.Context(), root, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(aged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aged owned bundle retained: %v", err)
	}
	for _, path := range []string{active, fresh, boundary, unmarked, linkedMarker, linkedBundle} {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("protected bundle removed: %s: %v", path, err)
		}
	}
	if data, err := os.ReadFile(custom); err != nil || string(data) != "keep capture" {
		t.Fatalf("custom capture changed: %q, %v", data, err)
	}
}
