package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Hold the actual managed-store lock, not a usage-specific test seam. Release
// runs before usage-owner cleanup even when a responsiveness assertion fails.
func holdUsageStoreLock(t *testing.T, store *mekugiReplayStore) func() {
	t.Helper()
	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- store.locked(context.Background(), func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case err := <-done:
		t.Fatalf("acquire managed-store lock: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("acquiring managed-store lock stalled")
	}
	var once sync.Once
	unlock := func() {
		once.Do(func() {
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("release managed-store lock: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("releasing managed-store lock stalled")
			}
		})
	}
	t.Cleanup(unlock)
	return unlock
}

func requireUsageOperationPrompt(t *testing.T, operation func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		operation()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("usage consumer waited for managed-store lock")
	}
}

func flushUsageForTest(t *testing.T, usage *threadUsage) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := usage.flush(ctx); err != nil {
		t.Fatalf("flush usage: %v", err)
	}
}

func TestThreadUsageAsyncConsumersIgnoreStoreContention(t *testing.T) {
	directory := t.TempDir()
	store := usageStoreFixture(t, directory)
	seed := storedUsageFixture(store)
	seed.markNew("retained")
	seed.observation("retained", "retained", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
	seed.close()

	u := storedUsageFixture(store)
	t.Cleanup(u.close)
	var notices atomic.Int64
	u.notice = func(string, error) { notices.Add(1) }
	unlock := holdUsageStoreLock(t, store)
	// An uncached retained identity must be readable even while another managed
	// writer owns store.lock. Snapshot consumers also remain live.
	requireUsageOperationPrompt(t, func() { u.restore("retained", true) })
	requireUsageOperationPrompt(t, func() { u.snapshot("retained") })
	requireStoredUsage(t, u, "retained", 10, 1, 1, .00003)
	u.markNew("live")
	requireUsageOperationPrompt(t, func() {
		u.observation("live", "live", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 20, UncachedInputTokens: 20, OutputTokens: 2})
	})
	requireUsageOperationPrompt(t, func() { u.snapshot("live") })
	report := requireStoredUsage(t, u, "live", 20, 2, 1, .00006)
	if report.priorUnknown {
		t.Fatal("normal lock contention erased a proven new baseline")
	}
	if _, err := os.Stat(filepath.Join(store.directory, threadUsageName("live"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("usage writer bypassed managed-store lock: %v", err)
	}
	// Deliberately exceed the removed 250 ms timeout with a real filesystem
	// lock. Synthetic time cannot establish cross-process lock behavior.
	<-time.After(300 * time.Millisecond)
	unlock()
	flushUsageForTest(t, u)
	if notices.Load() != 0 {
		t.Fatalf("ordinary contention emitted %d failure notices", notices.Load())
	}
	u.close()
	restarted := storedUsageFixture(usageStoreFixture(t, directory))
	t.Cleanup(restarted.close)
	requireStoredUsage(t, restarted, "live", 20, 2, 1, .00006)
	requireStoredUsage(t, restarted, "retained", 10, 1, 1, .00003)
}

func TestThreadUsageAsyncConcurrentOwnersPreservePricedDeltas(t *testing.T) {
	directory := t.TempDir()
	store := usageStoreFixture(t, directory)
	owners := []*threadUsage{storedUsageFixture(store), storedUsageFixture(store)}
	for _, owner := range owners {
		t.Cleanup(owner.close)
		owner.markNew("shared")
	}
	unlock := holdUsageStoreLock(t, store)
	var workers sync.WaitGroup
	for index, owner := range owners {
		workers.Go(func() {
			model, tier := "gpt-6-sol", "priority"
			if index == 1 {
				model, tier = "grok:grok-4.6", ""
			}
			for range 20 {
				observation := owner.observation("shared", "shared", model, tier)
				observation.reasoning = "high"
				counts := tokenCounts{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000}
				observation.observe(counts)
				observation.observe(counts)
				observation.finish()
			}
			owner.observation("shared", "shared", model, tier).finish()
		})
	}
	requireUsageOperationPrompt(t, workers.Wait)
	for index, owner := range owners {
		cost := 12.0
		if index == 1 {
			cost = 5.2
		}
		report := requireStoredUsage(t, owner, "shared", 2_000_000, 200_000, 21, cost)
		if report.missingUsage != 1 {
			t.Fatalf("owner %d lost missing terminal usage: %+v", index, report)
		}
	}
	unlock()
	for _, owner := range owners {
		flushUsageForTest(t, owner)
		// A second drain must not replay the already-applied queued deltas.
		flushUsageForTest(t, owner)
		owner.close()
	}
	restarted := storedUsageFixture(usageStoreFixture(t, directory))
	t.Cleanup(restarted.close)
	report := requireStoredUsage(t, restarted, "shared", 4_000_000, 400_000, 42, 17.2)
	if report.missingUsage != 2 || !strings.Contains(report.model, "gpt-6-sol high [fast]") || !strings.Contains(report.model, "grok:grok-4.6 high") {
		t.Fatalf("coalescing lost response-specific labels or usage gaps: %+v", report)
	}
}

func TestThreadUsageAsyncFlushCancellationOnlyCancelsWait(t *testing.T) {
	directory := t.TempDir()
	store := usageStoreFixture(t, directory)
	u := storedUsageFixture(store)
	t.Cleanup(u.close)
	unlock := holdUsageStoreLock(t, store)
	requireUsageOperationPrompt(t, func() {
		u.observation("child", "child", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- u.flush(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("flush returned with an outstanding blocked write: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled flush = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled flush continued waiting for storage")
	}
	// Neither the earlier pending delta nor subsequent accounting belongs to
	// the cancelled caller's lifetime.
	requireUsageOperationPrompt(t, func() {
		u.observation("child", "child", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 20, UncachedInputTokens: 20, OutputTokens: 2})
	})
	requireStoredUsage(t, u, "child", 30, 3, 2, .00009)
	unlock()
	flushUsageForTest(t, u)
	restarted := storedUsageFixture(usageStoreFixture(t, directory))
	t.Cleanup(restarted.close)
	requireStoredUsage(t, restarted, "child", 30, 3, 2, .00009)
}

func TestThreadUsageAsyncCloseDrainsBlockedWrites(t *testing.T) {
	directory := t.TempDir()
	store := usageStoreFixture(t, directory)
	u := storedUsageFixture(store)
	t.Cleanup(u.close)
	unlock := holdUsageStoreLock(t, store)
	requireUsageOperationPrompt(t, func() {
		u.observation("child", "child", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
	})
	done := make(chan struct{})
	go func() {
		u.close()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("close returned before queued usage became durable")
	case <-time.After(30 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not drain after storage became available")
	}
	restarted := storedUsageFixture(usageStoreFixture(t, directory))
	t.Cleanup(restarted.close)
	requireStoredUsage(t, restarted, "child", 10, 1, 1, .00003)
}
