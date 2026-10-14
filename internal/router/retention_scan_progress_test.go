package router

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Retained calls are shared by fork catalogs. Planning needs their envelope
// references, not their large inline output and edit evidence.
func retentionScanProgressFixture(tb testing.TB, owners int) *mekugiReplayStore {
	tb.Helper()
	store, err := openMekugiReplayStore(tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
	histories := make(map[string]mekugiHistory)
	for i := range 8 {
		histories[fmt.Sprint(i)] = mekugiHistory{ToolName: "shell", Script: "true", Report: strings.Repeat("retained output\n", 1<<16)}
	}
	for i := range owners {
		ctx, release, err := store.beginSession(tb.Context(), fmt.Sprint("fork-", i), "router")
		if err != nil {
			tb.Fatal(err)
		}
		if err := store.put(ctx, "/w", histories); err != nil {
			tb.Fatal(err)
		}
		if err := store.retainInput(ctx, "/w", nil, histories, nil); err != nil {
			tb.Fatal(err)
		}
		release()
	}
	return store
}

func TestRetentionScanProgress(t *testing.T) {
	store := retentionScanProgressFixture(t, 4)
	peer, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	plan, err := store.planStoragePrune(t.Context())
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("retention planning: calls=8 owners=4 allocated=%d elapsed=%s", allocated, time.Since(started))
	if err != nil || plan == nil || len(plan.snapshot.sessions) != 4 {
		t.Fatalf("planning: plan=%v err=%v", plan != nil, err)
	}
	for i := range 8 {
		if owners := plan.snapshot.owners[replayRecordName("/w", fmt.Sprint(i), false)]; owners != 4 {
			t.Fatalf("call %d has %d owners, want 4", i, owners)
		}
	}
	// Allow cold decoder buffers and runtime overhead, while rejecting geometric
	// record-buffer growth and repeated envelope decoding for each fork owner.
	if allocated > 32<<20 {
		t.Fatalf("envelope planning allocated %d bytes, budget %d", allocated, 32<<20)
	}
	for attempt := range 3 {
		if attempt > 0 {
			plan, err = store.planStoragePrune(t.Context())
			if err != nil {
				t.Fatal(err)
			}
		}
		// A different router publishes between planning and commit, just as
		// live maintenance can lose its view while stock calls are completing.
		retentionTestPut(t, peer, t.Context(), "/other", fmt.Sprint(attempt))
		committed, _, err := store.commitStoragePrune(t.Context(), plan, &plan.snapshot.sessions[0])
		if committed || !errors.Is(err, errStoragePruneStale) {
			t.Fatalf("accepted stale attempt %d: committed=%v err=%v", attempt, committed, err)
		}
	}
	for i := range 8 {
		retentionTestExists(t, store, "/w", fmt.Sprint(i), true)
	}
	for i := range 4 {
		retentionTestAge(t, store, fmt.Sprint("fork-", i), sessionRetention+time.Hour)
	}
	if err := store.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		retentionTestExists(t, store, "/w", fmt.Sprint(i), false)
	}
}

func BenchmarkRetentionScanProgress(b *testing.B) {
	for _, owners := range []int{1, 4} {
		b.Run(fmt.Sprint("owners=", owners), func(b *testing.B) {
			store := retentionScanProgressFixture(b, owners)
			b.ReportAllocs()
			for b.Loop() {
				plan, err := store.planStoragePrune(b.Context())
				if err != nil || plan == nil {
					b.Fatalf("planning: %v", err)
				}
			}
		})
	}
}
