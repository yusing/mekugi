package launch

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"

	"github.com/yusing/mekugi/libraries/persistence"
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
				if err := store.locked(t.Context(), func(store *mekugiReplayStore) error { return store.transaction.Write(name, "test-pending-", data) }); err != nil {
					t.Fatal(err)
				}
			}
			revision := func() string {
				t.Helper()
				var value string
				err := store.locked(t.Context(), func(store *mekugiReplayStore) error {
					var err error
					value, err = store.transaction.Revision()
					return err
				})
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

func TestPersistenceDebugStorageLocation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root, err := debugStorageDirectory()
	if err != nil || root != filepath.Join(os.Getenv("XDG_STATE_HOME"), "mekugi", "debug") {
		t.Fatalf("debug root = %q, %v", root, err)
	}
	bundle, err := testDiagnostics(nil, nil).OpenBundle(diagnostic.BundleOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	if filepath.Dir(filepath.Dir(bundle.Paths()[0])) != root {
		t.Fatal("bundle outside selected root")
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
	info, err := os.Stat(marker)
	if err != nil || time.Since(info.ModTime()) >= time.Hour {
		t.Fatalf("unchanged sweep marker was not refreshed: %v", err)
	}
}
