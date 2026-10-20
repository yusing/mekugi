package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestThreadUsageFailedPublicationCannotRestoreCompleteLifetime(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	u := storedUsageFixture(store)
	t.Cleanup(u.close)
	u.markNew("child")
	counts := tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1}
	u.observation("child", "child", "gpt-6-sol", "").observe(counts)
	flushUsageForTest(t, u)
	if report, _ := u.snapshot("child"); report.priorUnknown {
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
	u.observation("child", "child", "gpt-6-sol", "").observe(counts)
	flushUsageForTest(t, u)
	if report, _ := u.snapshot("child"); !report.priorUnknown || report.InputTokens != 20 {
		t.Fatalf("publication failure lost live evidence or claimed continuity: %+v", report)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	u.close()
	restored := storedUsageFixture(store)
	report := requireStoredUsage(t, restored, "child", 10, 1, 1, .00003)
	if !report.priorUnknown {
		t.Fatal("a stale durable record claimed complete lifetime usage after write failure")
	}
}

func TestThreadUsageAccountingBeforeHistoryRemainsUncertain(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	u := storedUsageFixture(store)
	t.Cleanup(u.close)
	u.observation("legacy", "legacy", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10})
	u.restore("legacy", true)
	u.markNew("legacy") // Late notifications cannot erase established uncertainty.
	if report, _ := u.snapshot("legacy"); !report.priorUnknown {
		t.Fatal("accounting before history hydration lost the unknown earlier window")
	}
	u.close()
	restored := storedUsageFixture(store)
	if report, _ := restored.snapshot("legacy"); !report.priorUnknown {
		t.Fatal("unclassified history became complete after restart")
	}
}

func TestThreadUsageStorageFailurePreservesLiveEvidence(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	u := storedUsageFixture(store)
	t.Cleanup(u.close)
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
	u.restore("child", true)
	for range 2 {
		u.observation("child", "child", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
	}
	report := requireStoredUsage(t, u, "child", 20, 2, 2, .00006)
	if !report.priorUnknown || notices != 1 {
		t.Fatalf("storage failure claimed completeness or repeated notices: %+v notices=%d", report, notices)
	}
	// Another thread's persistence remains independent of the failed target.
	u.observation("other", "other", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10})
	flushUsageForTest(t, u)
	restored := storedUsageFixture(store)
	requireStoredUsage(t, restored, "other", 10, 0, 1, .00002)
}

func TestThreadUsageStoreRejectsConflictingIdentityAndRetainsOwnership(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	u := storedUsageFixture(store)
	t.Cleanup(u.close)
	u.observation("source", "source", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10})
	flushUsageForTest(t, u)
	name := threadUsageName("source")
	if !retainedDataName(name) {
		t.Fatal("usage record excluded from managed storage")
	}
	retained, err := store.readRetainedSession(storageSessionName("source"))
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
	restored := storedUsageFixture(store)
	restored.restore("fork", true)
	got, _ := restored.snapshot("fork")
	if got.InputTokens != 0 || got.OutputTokens != 0 || got.roundtrips != 0 || !got.priorUnknown {
		t.Fatalf("conflicting durable identity borrowed consumption: %+v", got)
	}
	requireStoredUsage(t, restored, "source", 10, 0, 1, .00002)
}

func TestThreadUsageAsyncRetirementPreservesKnownLiveBaseline(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	u := storedUsageFixture(store)
	t.Cleanup(u.close)
	u.observation("child", "child", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
	flushUsageForTest(t, u)
	unlock := holdUsageStoreLock(t, store)
	u.observation("child", "child", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 20, UncachedInputTokens: 20, OutputTokens: 2})
	// Simulate retirement under its actual publication lock. Known consumption
	// in the live owner must survive even if its durable baseline disappears.
	if err := os.Remove(filepath.Join(store.directory, threadUsageName("child"))); err != nil {
		t.Fatal(err)
	}
	unlock()
	flushUsageForTest(t, u)
	requireStoredUsage(t, u, "child", 30, 3, 2, .00009)
	restarted := storedUsageFixture(store)
	requireStoredUsage(t, restarted, "child", 30, 3, 2, .00009)
}

func TestThreadUsageAsyncShutdownBudgetCancelsAndJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := usageStoreFixture(t, t.TempDir())
		u := storedUsageFixture(store)
		defer u.close()
		u.observation("child", "child", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1})
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
		u.observation("child", "child", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 20, UncachedInputTokens: 20, OutputTokens: 2})
		done := make(chan struct{})
		go func() { u.close(); close(done) }()
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
		restarted := storedUsageFixture(store)
		report := requireStoredUsage(t, restarted, "child", 10, 1, 1, .00003)
		if !report.priorUnknown {
			t.Fatal("restart claimed complete usage after exhausting shutdown budget")
		}
	})
}
