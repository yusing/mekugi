package router

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/responses"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func assertStorageAdmission(t *testing.T, store *mekugiReplayStore) {
	t.Helper()
	if err := store.locked(t.Context(), func(store *mekugiReplayStore) error {
		got, err := store.transaction.FileSizes()
		if err != nil {
			return err
		}
		want, err := store.testFileSizes()
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("admission differs from disk: got %v, want %v", got, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStorageAdmissionTracksScopedWritesAndFailedPublication(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := retentionTestSession(t, store, "current", 0)
	for _, commentary := range []bool{false, true} {
		name := replayRecordName("/w", "changing", commentary)
		for _, size := range []int{100, 20} {
			data := responses.MustMarshalJSON(replayRecord{Version: 1, Workspace: "/w", CallID: "changing", Commentary: commentary, History: mekugiHistory{Script: strings.Repeat("x", size)}})
			if err := store.locked(ctx, func(store *mekugiReplayStore) error {
				return store.scoped(ctx).writeManagedFile(name, "call-pending-", data)
			}); err != nil {
				t.Fatal(err)
			}
			assertStorageAdmission(t, store)
		}
	}
	err = store.locked(ctx, func(store *mekugiReplayStore) error {
		return store.scoped(ctx).writeManagedFile(replayRecordName("/w", "record", false), "missing-directory/pending-", []byte("record"))
	})
	if err == nil {
		t.Fatal("invalid publication succeeded")
	}
	assertStorageAdmission(t, store)
}

func TestStorageAdmissionRefreshesAfterPeerPublicationAndCleanup(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	peer, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release := retentionTestSession(t, peer, "old", 0)
	retentionTestPut(t, peer, ctx, "/w", "owned")
	release()
	retentionTestAge(t, peer, "old", 15*24*time.Hour)
	assertStorageAdmission(t, store)
	files, err := store.testFileSizes()
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes, _ = store.publication.Usage(store.retentionLimits(), files, "", 0)
	name := replayRecordName("/w", "peer-growth", false)
	data := responses.MustMarshalJSON(replayRecord{Version: 1, Workspace: "/w", CallID: "peer-growth", History: mekugiHistory{Script: strings.Repeat("x", 100)}})
	if err := peer.locked(t.Context(), func(peer *mekugiReplayStore) error { return peer.transaction.Write(name, "call-pending-", data) }); err != nil {
		t.Fatal(err)
	}
	err = store.locked(t.Context(), func(store *mekugiReplayStore) error { return store.maintainStorage("", 0) })
	var pressure *history.PressureError
	if !errors.As(err, &pressure) {
		t.Fatalf("peer growth did not enforce quota: %v", err)
	}
	assertStorageAdmission(t, store)
	if err := peer.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.locked(t.Context(), func(store *mekugiReplayStore) error { return store.maintainStorage("", 0) }); err != nil {
		t.Fatalf("peer cleanup left stale quota accounting: %v", err)
	}
	assertStorageAdmission(t, store)
}

func BenchmarkStorageAdmissionPublication(b *testing.B) {
	for _, cold := range []bool{false, true} {
		name := "cached"
		if cold {
			name = "rescan"
		}
		b.Run(name, func(b *testing.B) {
			store, err := openMekugiReplayStore(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			for i := range 1000 {
				name := replayRecordName("/w", fmt.Sprint(i), false)
				data := responses.MustMarshalJSON(replayRecord{Version: 1, Workspace: "/w", CallID: fmt.Sprint(i)})
				if err := os.WriteFile(filepath.Join(store.directory, name), data, 0600); err != nil {
					b.Fatal(err)
				}
			}
			ctx, release, err := store.beginSession(b.Context(), "current", "current")
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(release)
			record := replayRecordName("/w", "changing", false)
			iteration := 0
			b.ReportAllocs()
			for b.Loop() {
				data := responses.MustMarshalJSON(replayRecord{Version: 1, Workspace: "/w", CallID: "changing", History: mekugiHistory{Script: strconv.Itoa(iteration)}})
				if cold {
					store.publication = testHistory().Attach(store.directory)
				}
				err := store.locked(ctx, func(store *mekugiReplayStore) error {
					return store.scoped(ctx).writeManagedFile(record, "call-pending-", data)
				})
				if err != nil {
					b.Fatal(err)
				}
				iteration++
			}
		})
	}
}

// Independent disk oracle for cache-accounting assertions.
func (s *mekugiReplayStore) testFileSizes() (map[string]int64, error) {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return nil, err
	}
	files := make(map[string]int64)
	for _, entry := range entries {
		_, managed, err := testHistory().Kind(entry.Name())
		if err != nil {
			return nil, err
		}
		if !managed && !history.CatalogName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("non-regular managed record")
		}
		files[entry.Name()] = info.Size()
	}
	return files, nil
}
