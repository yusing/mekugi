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
)

func assertStorageAdmission(t *testing.T, store *mekugiReplayStore) {
	t.Helper()
	if err := store.locked(t.Context(), func() error {
		got, err := store.admissionFileSizes()
		if err != nil {
			return err
		}
		want, err := store.storageFileSizes()
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

func TestStorageAdmissionTracksScopedWritesAndFailedDependencies(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := retentionTestSession(t, store, "current", 0)
	for _, commentary := range []bool{false, true} {
		name := replayRecordName("/w", "changing", commentary)
		for _, size := range []int{100, 20} {
			data := mustMarshalJSON(replayRecord{Version: 1, Workspace: "/w", CallID: "changing", Commentary: commentary, History: mekugiHistory{Script: strings.Repeat("x", size)}})
			if err := store.locked(ctx, func() error {
				return store.scoped(ctx).writeManagedFile(name, "call-pending-", data)
			}); err != nil {
				t.Fatal(err)
			}
			assertStorageAdmission(t, store)
		}
	}
	good, bad := replayRecordName("/w", "dependency-one", false), replayRecordName("/w", "dependency-two", false)
	err = store.locked(ctx, func() error {
		return store.scoped(ctx).writeManagedFiles(managedFile{name: replayRecordName("/w", "record", false), pattern: "record-pending-", data: []byte("record")}, []string{good, bad}, []managedFile{
			{name: good, pattern: "dependency-pending-", data: []byte("first published dependency")},
			{name: bad, pattern: "missing-directory/pending-", data: []byte("unpublished dependency")},
		})
	})
	if err == nil {
		t.Fatal("invalid dependency publication succeeded")
	}
	if _, err := os.Stat(filepath.Join(store.directory, good)); err != nil {
		t.Fatalf("first dependency was not published: %v", err)
	}
	if store.admission.files != nil {
		t.Fatal("failed publication kept trusted admission accounting")
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
	files, err := store.storageFileSizes()
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes, _ = store.storageNeeds(files, "", 0)
	name := replayRecordName("/w", "peer-growth", false)
	data := mustMarshalJSON(replayRecord{Version: 1, Workspace: "/w", CallID: "peer-growth", History: mekugiHistory{Script: strings.Repeat("x", 100)}})
	if err := peer.locked(t.Context(), func() error { return peer.writeFile(name, "call-pending-", data) }); err != nil {
		t.Fatal(err)
	}
	err = store.locked(t.Context(), func() error { return store.maintainStorage("", 0) })
	var pressure *storagePressureError
	if !errors.As(err, &pressure) {
		t.Fatalf("peer growth did not enforce quota: %v", err)
	}
	assertStorageAdmission(t, store)
	if err := peer.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.locked(t.Context(), func() error { return store.maintainStorage("", 0) }); err != nil {
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
				data := mustMarshalJSON(replayRecord{Version: 1, Workspace: "/w", CallID: fmt.Sprint(i)})
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
				data := mustMarshalJSON(replayRecord{Version: 1, Workspace: "/w", CallID: "changing", History: mekugiHistory{Script: strconv.Itoa(iteration)}})
				err := store.locked(ctx, func() error {
					if cold {
						store.admission.files = nil
					}
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
