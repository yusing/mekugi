package router

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestThreadUsageFailedWriteCannotRestoreCompleteLifetime(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	u := storedUsageFixture(store)
	u.markNew("child")
	counts := tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 1}
	u.observation("child", "child", "gpt-6-sol", "").observe(counts)
	if report, _ := u.snapshot("child"); report.priorUnknown {
		t.Fatal("positive creation evidence did not establish a new baseline")
	}
	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- store.locked(context.Background(), func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	u.observation("child", "child", "gpt-6-sol", "").observe(counts)
	close(release)
	if err := <-done; err != nil {
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
	restored := storedUsageFixture(store)
	requireStoredUsage(t, restored, "other", 10, 0, 1, .00002)
}

func TestThreadUsageStoreRejectsConflictingIdentityAndRetainsOwnership(t *testing.T) {
	store := usageStoreFixture(t, t.TempDir())
	u := storedUsageFixture(store)
	u.observation("source", "source", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10})
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
