package accounting

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/contracts/inference"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestThreadUsageFailedPublicationCannotRestoreCompleteLifetime(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	u := storedUsageFixture(t, store)

	u.MarkNew("child")
	counts := inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1}
	u.Observation("child", "child", "gpt-6-sol", "").Observe(counts)
	flushUsageForTest(t, u)
	if report, _ := u.Snapshot("child"); report.PriorUnknown {
		t.Fatal("positive creation evidence did not establish a new baseline")
	}
	// Actual storage corruption, not ordinary contention, prevents publication.
	lockPath := filepath.Join(store.directory, "store.lock")
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockPath, 0700); err != nil {
		t.Fatal(err)
	}
	u.Observation("child", "child", "gpt-6-sol", "").Observe(counts)
	flushUsageForTest(t, u)
	if report, _ := u.Snapshot("child"); !report.PriorUnknown || report.InputTokens != 20 {
		t.Fatalf("publication failure lost live evidence or claimed continuity: %+v", report)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	u.Close()
	restored := storedUsageFixture(t, store)
	report := requireStoredUsage(t, restored, "child", 10, 1, 1, .00003)
	if !report.PriorUnknown {
		t.Fatal("a stale durable record claimed complete lifetime usage after write failure")
	}
}

func TestThreadUsageAccountingBeforeHistoryRemainsUncertain(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	u := storedUsageFixture(t, store)

	u.Observation("legacy", "legacy", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10})
	u.Restore("legacy", true)
	u.MarkNew("legacy") // Late notifications cannot erase established uncertainty.
	if report, _ := u.Snapshot("legacy"); !report.PriorUnknown {
		t.Fatal("accounting before history hydration lost the unknown earlier window")
	}
	u.Close()
	restored := storedUsageFixture(t, store)
	if report, _ := restored.Snapshot("legacy"); !report.PriorUnknown {
		t.Fatal("unclassified history became complete after restart")
	}
}

func TestThreadUsageStorageFailurePreservesLiveEvidence(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	u := storedUsageFixture(t, store)

	// Corrupt retained evidence is unreadable, not an empty baseline.
	if err := os.WriteFile(filepath.Join(store.directory, threadUsageName("child")), []byte(`{broken`), 0600); err != nil {
		t.Fatal(err)
	}
	notices := 0
	u.notice = func(thread string, err error) {
		if thread != "child" || err == nil {
			t.Fatalf("incorrect storage notice: %s %v", thread, err)
		}
		notices++
	}
	u.Restore("child", true)
	for range 2 {
		u.Observation("child", "child", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
	}
	report := requireStoredUsage(t, u, "child", 20, 2, 2, .00006)
	if !report.PriorUnknown || notices != 1 {
		t.Fatalf("storage failure claimed completeness or repeated notices: %+v notices=%d", report, notices)
	}
	// Another thread's persistence remains independent of the failed target.
	u.Observation("other", "other", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10})
	flushUsageForTest(t, u)
	restored := storedUsageFixture(t, store)
	requireStoredUsage(t, restored, "other", 10, 0, 1, .00002)
}

func TestThreadUsageStoreRejectsConflictingIdentityAndRetainsOwnership(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	u := storedUsageFixture(t, store)

	u.Observation("source", "source", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10})
	flushUsageForTest(t, u)
	name := threadUsageName("source")
	if _, found, err := testHistory().Kind(name); err != nil || !found {
		t.Fatal("usage record excluded from managed storage")
	}
	retained, err := store.publication.ReadCatalog(history.SessionName("source"))
	if err != nil || !retained.Files[name] {
		t.Fatalf("usage record lacks thread ownership: %+v %v", retained, err)
	}
	data, err := os.ReadFile(filepath.Join(store.directory, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.directory, threadUsageName("fork")), data, 0600); err != nil {
		t.Fatal(err)
	}
	restored := storedUsageFixture(t, store)
	restored.Restore("fork", true)
	got, _ := restored.Snapshot("fork")
	if got.InputTokens != 0 || got.OutputTokens != 0 || got.Roundtrips != 0 || !got.PriorUnknown {
		t.Fatalf("conflicting durable identity borrowed consumption: %+v", got)
	}
	requireStoredUsage(t, restored, "source", 10, 0, 1, .00002)
}

func TestThreadUsageAsyncRetirementPreservesKnownLiveBaseline(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	u := storedUsageFixture(t, store)

	u.Observation("child", "child", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
	flushUsageForTest(t, u)
	unlock := holdUsageStoreLock(t, store)
	u.Observation("child", "child", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 20, UncachedInputTokens: 20, OutputTokens: 2})
	// Simulate retirement under its actual publication lock. Known consumption
	// in the live owner must survive even if its durable baseline disappears.
	if err := os.Remove(filepath.Join(store.directory, threadUsageName("child"))); err != nil {
		t.Fatal(err)
	}
	unlock()
	flushUsageForTest(t, u)
	requireStoredUsage(t, u, "child", 30, 3, 2, .00009)
	restarted := storedUsageFixture(t, store)
	requireStoredUsage(t, restarted, "child", 30, 3, 2, .00009)
}

func TestThreadUsageAsyncShutdownBudgetCancelsAndJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := usageStoreFixture(t, t.TempDir())
		u := storedUsageFixture(t, store)
		defer u.Close()
		u.Observation("child", "child", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
		flushUsageForTest(t, u)
		var notices atomic.Int64
		u.notice = func(thread string, err error) {
			if thread != "child" || !errors.Is(err, context.Canceled) {
				t.Errorf("shutdown publication notice: thread=%q err=%v", thread, err)
			}
			notices.Add(1)
		}
		unlock := holdUsageStoreLock(t, store)
		defer unlock()
		u.Observation("child", "child", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 20, UncachedInputTokens: 20, OutputTokens: 2})
		done := make(chan struct{})
		go func() { u.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(shutdownTimeout + 5*time.Second):
			t.Fatal("shutdown failed to cancel and join the blocked usage writer")
		}
		if notices.Load() != 1 {
			t.Fatalf("shutdown lost its unretained-usage notice: %d", notices.Load())
		}
		// A joined writer cannot publish later merely because contention clears.
		unlock()
		restarted := storedUsageFixture(t, store)
		report := requireStoredUsage(t, restarted, "child", 10, 1, 1, .00003)
		if !report.PriorUnknown {
			t.Fatal("restart claimed complete usage after exhausting shutdown budget")
		}
	})
}
