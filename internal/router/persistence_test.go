package router

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/internal/persistence"
)

func TestPersistenceRecordNoOpAndChangedWrites(t *testing.T) {
	for _, tc := range []struct {
		name           string
		first, changed []byte
	}{
		{"json", []byte(`{"value":1}`), []byte(`{"value":2}`)},
		{"binary", []byte{0, 255, 128, 1}, []byte{0, 254, 128, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := usageStoreFixture(t, t.TempDir())
			store.writes = new(persistence.Counter)
			name := "test-record"
			write := func(data []byte) {
				t.Helper()
				if err := store.locked(t.Context(), func() error { return store.writeFile(name, "test-pending-", data) }); err != nil {
					t.Fatal(err)
				}
			}
			revision := func() string {
				t.Helper()
				value, err := store.storageRevision()
				if err != nil {
					t.Fatal(err)
				}
				return value
			}
			write(tc.first)
			firstRevision := revision()
			firstBytes := store.writes.Snapshot().Bytes
			if firstBytes <= uint64(len(tc.first)) {
				t.Fatalf("missing revision write accounting: %d", firstBytes)
			}
			path := filepath.Join(store.directory, name)
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			write(bytes.Clone(tc.first))
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if store.writes.Snapshot().Bytes != firstBytes || revision() != firstRevision || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("identical record rewrote bytes, revision, or filesystem identity")
			}
			write(tc.changed)
			if revision() == firstRevision || store.writes.Snapshot().Bytes <= firstBytes+uint64(len(tc.changed)) {
				t.Fatal("changed record did not publish and account a new revision")
			}
			reopened := usageStoreFixture(t, store.directory)
			got, err := os.ReadFile(filepath.Join(reopened.directory, name))
			if err != nil || !bytes.Equal(got, tc.changed) {
				t.Fatalf("reopened record = %v, %v", got, err)
			}
			entries, err := os.ReadDir(store.directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if bytes.HasPrefix([]byte(entry.Name()), []byte("test-pending-")) {
					t.Fatalf("unpublished temporary file: %s", entry.Name())
				}
			}
		})
	}
}

func TestPersistenceUsageCoalescesBurstAndRetainsDeltas(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	store.writes = new(persistence.Counter)
	synctest.Test(t, func(t *testing.T) {
		usage := storedUsageFixture(store)
		defer usage.close()
		usage.markNew("burst")
		for range 10 {
			usage.observation("burst", "burst", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 2})
		}
		synctest.Wait()
		time.Sleep(24 * time.Millisecond)
		synctest.Wait()
		if _, err := os.Stat(filepath.Join(store.directory, threadUsageName("burst"))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("usage published before coalescing window: %v", err)
		}
		if store.writes.Snapshot().Bytes != 0 {
			t.Fatal("coalescing window wrote managed bytes")
		}
		flushUsageForTest(t, usage)
		retained, err := readThreadUsage(store, "burst")
		if err != nil || retained == nil {
			t.Fatalf("retained usage: %v, %v", retained, err)
		}
		if retained.roundtrips != 10 || retained.counts.InputTokens != 100 || retained.counts.OutputTokens != 20 {
			t.Fatalf("burst lost or duplicated pending deltas: %+v", retained)
		}
		record, err := os.ReadFile(filepath.Join(store.directory, threadUsageName("burst")))
		if err != nil {
			t.Fatal(err)
		}
		revision, err := os.ReadFile(filepath.Join(store.directory, "storage-revision"))
		if err != nil {
			t.Fatal(err)
		}
		catalog, err := os.ReadFile(filepath.Join(store.directory, storageSessionName("burst")))
		if err != nil {
			t.Fatal(err)
		}
		// The first usage publication also retains its session catalog. Each
		// changed record advances the revision once; the burst needs only one
		// usage record and one catalog publication.
		if got, want := store.writes.Snapshot().Bytes, uint64(len(record)+len(catalog)+2*len(revision)); got != want {
			t.Fatalf("burst wrote %d bytes, want one publication of %d", got, want)
		}
		for range 3 {
			usage.observation("burst", "burst", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 2})
		}
		flushUsageForTest(t, usage)
		retained, err = readThreadUsage(store, "burst")
		if err != nil || retained == nil || retained.roundtrips != 13 || retained.counts.InputTokens != 130 || retained.counts.OutputTokens != 26 {
			t.Fatalf("followup burst lost or duplicated deltas: %+v, %v", retained, err)
		}
	})
}

func TestPersistenceDebugStorageLocation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root, err := debugStorageDirectory()
	if err != nil || root != filepath.Join(os.Getenv("XDG_STATE_HOME"), "mekugi", "debug") {
		t.Fatalf("debug root = %q, %v", root, err)
	}
	directory, release, err := createDebugBundle()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	})
	if filepath.Dir(directory) != root {
		t.Fatalf("bundle outside state root: %s", directory)
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("bundle permissions: %v, %v", info, err)
	}
	t.Setenv("XDG_STATE_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err = debugStorageDirectory()
	if err != nil || root != filepath.Join(home, ".local", "state", "mekugi", "debug") {
		t.Fatalf("default debug root = %q, %v", root, err)
	}
}

func TestPersistenceDebugRetentionRespectsOwnershipAndLease(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Now()
	old := now.Add(-14*24*time.Hour - time.Second)
	makeBundle := func() (string, func() error) {
		t.Helper()
		directory, release, err := createDebugBundle()
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
	root := filepath.Dir(aged)
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
	if err := cleanupDebugBundles(t.Context(), now); err != nil {
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

func TestPersistenceRetentionRefreshesUnchangedSweepMarker(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	if err := store.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(store.directory, "retention-sweep")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}
	if err := store.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	plan, err := store.planStoragePrune(t.Context())
	if err != nil || plan != nil {
		t.Fatalf("unchanged sweep marker did not suppress repeated scans: %v, %v", plan, err)
	}
}
