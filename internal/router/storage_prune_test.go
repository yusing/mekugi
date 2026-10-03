package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBackgroundPruneDoesNotBlockSessionStartAndRejectsStaleOwnership(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent, releaseParent := retentionTestSession(t, store, "parent", 0)
	h := snapshotTestHistory()
	snapshotTestPut(t, store, parent, "shared", h)
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
	if err := store.retainInput(child, "/w", nil, map[string]mekugiHistory{"shared": h}, nil); err != nil {
		t.Fatal(err)
	}
	close(resume)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not retry its stale plan")
	}
	if _, err := os.Stat(filepath.Join(store.directory, storageSessionName("parent"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired parent ownership was not reclaimed")
	}
	retentionTestExists(t, store, "/w", "shared", true)
}

func TestBackgroundPruneCommitsBoundedBatchesAndPreservesSnapshotDependencies(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release := retentionTestSession(t, store, "old", 0)
	h := snapshotTestHistory()
	for i := range 35 {
		snapshotTestPut(t, store, ctx, fmt.Sprintf("call-%d", i), h)
	}
	release()
	retentionTestAge(t, store, "old", 15*24*time.Hour)
	plan, err := store.planStoragePrune(t.Context())
	if err != nil || plan == nil {
		t.Fatalf("planning: %v", err)
	}
	var candidate *storageCandidate
	for i := range plan.snapshot.sessions {
		if plan.snapshot.sessions[i].thread == "old" {
			candidate = &plan.snapshot.sessions[i]
		}
	}
	if candidate == nil {
		t.Fatal("missing expired candidate")
	}
	before := len(candidate.files)
	committed, _, err := store.commitStoragePrune(t.Context(), plan, candidate)
	if err != nil || !committed || before-len(candidate.files) != storagePruneBatchFiles {
		t.Fatalf("unbounded prune commit: committed=%v removed=%d err=%v", committed, before-len(candidate.files), err)
	}
	remaining := 0
	for i := range 35 {
		if _, found, err := store.lookup(t.Context(), "/w", fmt.Sprintf("call-%d", i)); err != nil {
			t.Fatal(err)
		} else if found {
			remaining++
		}
	}
	if remaining != 35-storagePruneBatchFiles || len(snapshotTestBlobs(t, store)) != 3 {
		t.Fatal("batch destroyed dependencies still needed by surviving calls")
	}
	restarted, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(snapshotTestBlobs(t, store)) != 0 {
		t.Fatal("last owner left snapshot objects behind")
	}
}

func TestBackgroundPruneLargeSessionCatalogWriteAmplification(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const count = 10_000
	catalog := retainedSession{Version: 1, Thread: "large", LastUsed: time.Now().Add(-sessionRetention - time.Hour), Files: make(map[string]bool, count)}
	for i := range count {
		name := fmt.Sprintf("output-%064x.json", i)
		catalog.Files[name] = true
		if err := os.WriteFile(filepath.Join(store.directory, name), []byte("retained output fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := marshalProtocolJSON(catalog)
	if err != nil {
		t.Fatal(err)
	}
	name := storageSessionName(catalog.Thread)
	if err := store.locked(t.Context(), func() error { return store.writeFile(name, "session-pending-", data) }); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.directory, name)
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.planStoragePrune(t.Context())
	if err != nil || plan == nil || len(plan.snapshot.sessions) != 1 {
		t.Fatalf("large catalog planning: %v", err)
	}
	candidate := &plan.snapshot.sessions[0]
	batches, written, freed := 0, int64(0), int64(0)
	var longest time.Duration
	for len(candidate.files) > 0 {
		before, started := len(candidate.files), time.Now()
		committed, bytes, err := store.commitStoragePrune(t.Context(), plan, candidate)
		longest = max(longest, time.Since(started))
		if err != nil || !committed || before-len(candidate.files) > storagePruneBatchFiles {
			t.Fatalf("large catalog commit: committed=%v err=%v", committed, err)
		}
		batches++
		freed += bytes
		current, err := os.Stat(path)
		if len(candidate.files) == 0 {
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("completed ownership was not removed: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		// Atomic publication replaces the inode. Inspect the real persisted
		// catalog rather than counting calls to a mocked writer.
		if !os.SameFile(original, current) {
			written += current.Size()
			original = current
		}
	}
	t.Logf("persisted prune workload: files=%d batches=%d catalog_bytes_rewritten=%d reclaimed=%d longest_batch=%s", count, batches, written, freed, longest)
	if written != 0 || batches != count/storagePruneBatchFiles || freed != int64(len(data)+count*len("retained output fixture")) {
		t.Fatal("catalog write amplification or incomplete reclamation")
	}
	// The entire measured commit bounds its publication-lock interval. This
	// budget deliberately tolerates slow/race-enabled CI filesystems.
	if longest > 500*time.Millisecond {
		t.Fatalf("publication batch exceeded responsiveness budget: %s", longest)
	}
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
	if _, err := os.Stat(filepath.Join(store.directory, storageSessionName("empty-old"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired empty catalog survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.directory, storageSessionName("empty-active"))); err != nil {
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
	files, err := store.storageFileSizes()
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes, _ = store.storageNeeds(files, "", 0)
	write := func() error {
		return store.put(current, "/w", map[string]mekugiHistory{"new": {ToolName: "shell", Script: "true"}})
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
	leased, release, err := store.tryPruneLease("retention-maintenance.lock")
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
