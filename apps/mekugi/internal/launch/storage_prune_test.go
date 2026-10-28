package launch

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestBackgroundPruneDoesNotBlockSessionStartAndRejectsStaleOwnership(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		store, err := openMekugiReplayStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		parent, releaseParent := retentionTestSession(t, store, "parent", 0)
		// A historical envelope depends on a separate blob. Fork adoption must
		// protect both after the background worker has planned parent removal.
		snapshot := []byte(`{"Exec":[{"Path":"/w/text","Kind":"text","Content":"before\n"}]}`)
		blob := legacycall.SnapshotName(snapshot)
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(snapshot); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := store.scoped(parent).locked(parent, func(selected *mekugiReplayStore) error {
			if err := writeRetainedRecord(selected.transaction, selected.session.Thread, selected.retentionLimits(), blob, "snapshot-pending-", compressed.Bytes()); err != nil {
				return err
			}
			return selected.transaction.PublishHostCall(selected.session.Thread, selected.retentionLimits(), legacycall.Record{
				Version: 2, Workspace: "/w", CallID: "shared",
				History:   legacycall.History{ToolName: "exec", ExecObservation: &legacycall.Observation{Class: "scoped"}},
				Snapshots: &legacycall.Snapshots{ExecFiles: blob},
			}, false)
		}); err != nil {
			t.Fatal(err)
		}
		record, found, err := store.publication.ReadHostCall("/w", "shared", false)
		if err != nil || !found {
			t.Fatalf("historical fixture: found=%v err=%v", found, err)
		}
		h := record.History
		releaseParent()
		retentionTestAge(t, store, "parent", 15*24*time.Hour)
		ctx, cancel := context.WithCancel(t.Context())
		phase, resume, finished, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
		var planned, completed sync.Once
		store.storageNotice = func(_, _, _ string, message string) {
			if strings.Contains(message, "inspected") {
				planned.Do(func() {
					close(phase)
					select {
					case <-resume:
					case <-ctx.Done():
					}
				})
			}
			if strings.Contains(message, "reclaimed") {
				completed.Do(func() { close(finished) })
			}
		}
		go func() { defer close(done); store.runRetentionSweeps(ctx, nil) }()
		defer func() {
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("background worker did not stop with its owner")
			}
		}()
		select {
		case <-phase:
		case <-time.After(5 * time.Second):
			t.Fatal("background planning did not start")
		}
		// The worker is parked after its actual persisted catalog/index reads,
		// before reclamation. UI/session startup must not wait for this work.
		startCtx, stop := context.WithTimeout(t.Context(), 250*time.Millisecond)
		defer stop()
		started := time.Now()
		child, releaseChild, err := store.beginSession(startCtx, "fork", "different-router")
		if err != nil {
			t.Fatalf("session startup blocked by background pruning: %v", err)
		}
		defer releaseChild()
		t.Logf("session start while background plan paused: %s", time.Since(started))
		// Refresh only the caller's lifetime; keep the identity established above.
		child = context.WithValue(t.Context(), storageSessionKey{}, child.Value(storageSessionKey{}))
		if err := store.retainInput(child, "/w", nil, map[string]legacycall.History{"shared": h}, nil); err != nil {
			t.Fatal(err)
		}
		close(resume)
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not retry its stale plan")
		}
		if _, err := os.Stat(filepath.Join(store.directory, history.SessionName("parent"))); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("expired parent ownership was not reclaimed")
		}
		retentionTestExists(t, store, "/w", "shared", true)
		retained, found, err := store.publication.ReadHostCall("/w", "shared", false)
		if err != nil || !found || retained.History.ExecObservation == nil || len(retained.History.ExecObservation.Files) != 1 || retained.History.ExecObservation.Files[0].Content != "before\n" {
			t.Fatalf("adopted snapshot unavailable after parent pruning: found=%v err=%v history=%+v", found, err, retained.History)
		}
	})
}

func TestBackgroundPruneExpiresEmptyCatalogsWithoutRemovingActiveOwnership(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, releaseOld := retentionTestSession(t, store, "empty-old", 0)
	releaseOld()
	retentionTestAge(t, store, "empty-old", 15*24*time.Hour)
	_, releaseActive := retentionTestSession(t, store, "empty-active", 0)
	defer releaseActive()
	retentionTestAge(t, store, "empty-active", 15*24*time.Hour)
	if err := store.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.directory, history.SessionName("empty-old"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired empty catalog survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.directory, history.SessionName("empty-active"))); err != nil {
		t.Fatalf("active empty ownership was removed: %v", err)
	}
}

func TestBackgroundPrunePressureIsQueuedWithoutForegroundDeletion(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old, releaseOld := retentionTestSession(t, store, "old", 0)
	retentionTestPut(t, store, old, "/w", "old")
	releaseOld()
	retentionTestAge(t, store, "old", time.Hour)
	current, _ := retentionTestSession(t, store, "current", 0)
	files, err := store.testFileSizes()
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes, _ = store.publication.Usage(store.retentionLimits(), files, "", 0)
	write := func() error {
		return store.put(current, "/w", map[string]legacycall.History{"new": {ToolName: "shell", Script: "true"}})
	}
	if err := write(); err == nil || !strings.Contains(err.Error(), "do not rerun the host operation") {
		t.Fatalf("missing pending-cleanup diagnosis: %v", err)
	}
	retentionTestExists(t, store, "/w", "old", true)
	retentionTestExists(t, store, "/w", "new", false)
	ctx, cancel := context.WithCancel(t.Context())
	done, reclaimed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	store.storageNotice = func(_, _, _ string, message string) {
		if strings.Contains(message, "reclaimed") {
			once.Do(func() { close(reclaimed) })
		}
	}
	go func() { defer close(done); store.runRetentionSweeps(ctx, nil) }()
	defer func() { cancel(); <-done }()
	select {
	case <-reclaimed:
	case <-time.After(5 * time.Second):
		t.Fatal("background worker did not reclaim pressure request")
	}
	if err := write(); err != nil {
		t.Fatalf("persistence retry after background reclaim: %v", err)
	}
	retentionTestExists(t, store, "/w", "old", false)
	retentionTestExists(t, store, "/w", "new", true)
}

func TestBackgroundPruneCancellationAndCrossProcessLease(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	leased, release, err := store.publication.TryPruneLease("retention-maintenance.lock")
	if err != nil || !leased {
		t.Fatal(err)
	}
	defer release()
	other, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.directory, "retention-sweep")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a second worker ran while the maintenance lease was held")
	}
	release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.cleanupSessions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled maintenance continued: %v", err)
	}
}

func TestBackgroundPruneDelayedAgeRetryPressureAndRecovery(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "pressure", true: "restart"}[recovery], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store, err := openMekugiReplayStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				old, release := retentionTestSession(t, store, "old", 0)
				retentionTestPut(t, store, old, "/w", "old")
				release()
				retentionTestAge(t, store, "old", sessionRetention+time.Hour)
				current, _ := retentionTestSession(t, store, "current", 0)
				plans := 0
				notice := func(_, _, phase, _ string) {
					if phase == "planning" {
						plans++
						if plans == 1 {
							retentionTestPut(t, store, current, "/w", "active")
						}
					}
				}
				store.storageNotice = notice
				// The worker snapshots fixture limits while pressure changes them.
				var limitsMu sync.Mutex
				runSweeps := func(ctx context.Context) {
					runStorageRetention(ctx, testDiagnostics(nil, nil), func() retentionScope {
						limitsMu.Lock()
						defer limitsMu.Unlock()
						return store.retentionScope(ctx)
					}, nil)
				}
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() { defer close(done); runSweeps(ctx) }()
				defer func() { cancel(); <-done }()
				synctest.Wait()
				time.Sleep(5 * time.Second)
				synctest.Wait()
				if plans != 1 {
					cancel()
					<-done
					t.Fatalf("retried age planning before delay: %d plans", plans)
				}
				retentionTestExists(t, store, "/w", "old", true)
				if recovery {
					cancel()
					<-done // Cancellation does not wait for the delayed retry.
					store, err = openMekugiReplayStore(store.directory)
					if err != nil {
						t.Fatal(err)
					}
					store.storageNotice = notice
					ctx, cancel = context.WithCancel(t.Context())
					done = make(chan struct{})
					go func() { defer close(done); runSweeps(ctx) }()
					synctest.Wait()
				} else {
					files, err := store.testFileSizes()
					if err != nil {
						t.Fatal(err)
					}
					limitsMu.Lock()
					store.maxBytes, _ = store.publication.Usage(store.retentionLimits(), files, "", 0)
					limitsMu.Unlock()
					written := make(chan error, 1)
					go func() {
						values := map[string]legacycall.History{"new": {ToolName: "shell", Script: "true"}}
						err := store.put(current, "/w", values)
						if _, ok := errors.AsType[*history.PressureError](err); !ok {
							written <- err
							return
						}
						for {
							time.Sleep(100 * time.Millisecond)
							pending := false
							err = store.publication.Transaction(current, store.writes, func(tx *history.Transaction) error { var err error; pending, err = tx.PendingCleanup(); return err })
							if err != nil {
								written <- err
								return
							}
							if !pending {
								written <- store.put(current, "/w", values)
								return
							}
						}
					}()
					synctest.Wait()
					retentionTestExists(t, store, "/w", "new", false)
					select {
					case err := <-written:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(2 * time.Second):
						t.Fatal("pressure waited for the delayed age retry")
					}
					synctest.Wait()
					retentionTestExists(t, store, "/w", "new", true)
				}
				cancel()
				<-done
				if plans != 2 {
					t.Fatalf("recovery plans=%d, want 2", plans)
				}
				retentionTestExists(t, store, "/w", "old", false)
				retentionTestExists(t, store, "/w", "active", true)
			})
		})
	}
}

func (s *mekugiReplayStore) cleanupSessions(ctx context.Context) error {
	if s == nil {
		return nil
	}
	scope := s.retentionScope(ctx)
	return scope.store.Cleanup(ctx, scope.writes, scope.limits, scope.thread, scope.notice)
}
