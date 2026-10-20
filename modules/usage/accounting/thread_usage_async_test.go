package accounting

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

	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Hold the actual managed-store lock, not a usage-specific test seam. Release
// runs before usage-owner cleanup even when a responsiveness assertion fails.
func holdUsageStoreLock(t *testing.T, store *usageTestStore) func() {
	t.Helper()
	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- store.publication.Transaction(context.Background(), nil, func(_ *history.Transaction) error {
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

func flushUsageForTest(t *testing.T, usage *Service) {
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
	seed := storedUsageFixture(t, store)
	seed.MarkNew("retained")
	seed.Observation("retained", "retained", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
	seed.Close()

	u := storedUsageFixture(t, store)

	var notices atomic.Int64
	u.notice = func(string, error) { notices.Add(1) }
	unlock := holdUsageStoreLock(t, store)
	// An uncached retained identity must be readable even while another managed
	// writer owns store.lock. Snapshot consumers also remain live.
	requireUsageOperationPrompt(t, func() { u.Restore("retained", true) })
	requireUsageOperationPrompt(t, func() { u.Snapshot("retained") })
	requireStoredUsage(t, u, "retained", 10, 1, 1, .00003)
	u.MarkNew("live")
	requireUsageOperationPrompt(t, func() {
		u.Observation("live", "live", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 20, UncachedInputTokens: 20, OutputTokens: 2})
	})
	requireUsageOperationPrompt(t, func() { u.Snapshot("live") })
	report := requireStoredUsage(t, u, "live", 20, 2, 1, .00006)
	if report.PriorUnknown {
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
	u.Close()
	restarted := storedUsageFixture(t, usageStoreFixture(t, directory))

	requireStoredUsage(t, restarted, "live", 20, 2, 1, .00006)
	requireStoredUsage(t, restarted, "retained", 10, 1, 1, .00003)
}

func TestThreadUsageAsyncConcurrentOwnersPreservePricedDeltas(t *testing.T) {
	directory := t.TempDir()
	store := usageStoreFixture(t, directory)
	owners := []*Service{storedUsageFixture(t, store), storedUsageFixture(t, store)}
	for _, owner := range owners {

		owner.MarkNew("shared")
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
				observation := owner.Observation("shared", "shared", model, tier)
				observation.Reasoning = "high"
				counts := inference.Usage{InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 10_000}
				observation.Observe(counts)
				observation.Observe(counts)
				observation.Finish()
			}
			owner.Observation("shared", "shared", model, tier).Finish()
		})
	}
	requireUsageOperationPrompt(t, workers.Wait)
	for index, owner := range owners {
		cost := 12.0
		if index == 1 {
			cost = 5.2
		}
		report := requireStoredUsage(t, owner, "shared", 2_000_000, 200_000, 21, cost)
		if report.MissingUsage != 1 {
			t.Fatalf("owner %d lost missing terminal usage: %+v", index, report)
		}
	}
	unlock()
	for _, owner := range owners {
		flushUsageForTest(t, owner)
		// A second drain must not replay the already-applied queued deltas.
		flushUsageForTest(t, owner)
		owner.Close()
	}
	restarted := storedUsageFixture(t, usageStoreFixture(t, directory))

	report := requireStoredUsage(t, restarted, "shared", 4_000_000, 400_000, 42, 17.2)
	if report.MissingUsage != 2 || !strings.Contains(report.Model, "gpt-6-sol high [fast]") || !strings.Contains(report.Model, "grok:grok-4.6 high") {
		t.Fatalf("coalescing lost response-specific labels or usage gaps: %+v", report)
	}
}

func TestThreadUsageAsyncFlushCancellationOnlyCancelsWait(t *testing.T) {
	directory := t.TempDir()
	store := usageStoreFixture(t, directory)
	u := storedUsageFixture(t, store)

	unlock := holdUsageStoreLock(t, store)
	requireUsageOperationPrompt(t, func() {
		u.Observation("child", "child", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
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
		u.Observation("child", "child", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 20, UncachedInputTokens: 20, OutputTokens: 2})
	})
	requireStoredUsage(t, u, "child", 30, 3, 2, .00009)
	unlock()
	flushUsageForTest(t, u)
	restarted := storedUsageFixture(t, usageStoreFixture(t, directory))

	requireStoredUsage(t, restarted, "child", 30, 3, 2, .00009)
}

func TestThreadUsageAsyncCloseDrainsBlockedWrites(t *testing.T) {
	directory := t.TempDir()
	store := usageStoreFixture(t, directory)
	u := storedUsageFixture(t, store)

	unlock := holdUsageStoreLock(t, store)
	requireUsageOperationPrompt(t, func() {
		u.Observation("child", "child", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
	})
	done := make(chan struct{})
	go func() {
		u.Close()
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
	restarted := storedUsageFixture(t, usageStoreFixture(t, directory))

	requireStoredUsage(t, restarted, "child", 10, 1, 1, .00003)
}
