package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
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

// Keep the diagnosis workload, but publish on a fixed cadence rather than
// waiting for each new plan: a delayed age retry must coalesce those writes.
func TestRetentionWorkerCoalescesPublicationConflicts(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, conflicts := range []int{0, 4} {
		t.Run(fmt.Sprintf("publications=%d", conflicts), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := retentionScanProgressFixture(t, 4)
				for i := range 4 {
					retentionTestAge(t, store, fmt.Sprint("fork-", i), sessionRetention+time.Hour)
				}
				peer, err := openMekugiReplayStore(store.directory)
				if err != nil {
					t.Fatal(err)
				}
				peerCtx, release := retentionTestSession(t, peer, "independent-active", 0)
				defer release()
				plans, publications := 0, 0
				var publicationAlloc uint64
				publish := func() {
					var before, after runtime.MemStats
					runtime.ReadMemStats(&before)
					retentionTestPut(t, peer, peerCtx, "/independent", fmt.Sprint(publications))
					runtime.ReadMemStats(&after)
					publicationAlloc += after.TotalAlloc - before.TotalAlloc
					publications++
				}
				inject := conflicts > 0
				store.storageNotice = func(_, _, phase, _ string) {
					if phase == "planning" {
						plans++
						if inject {
							publish()
							inject = false
						}
					}
				}
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				started := time.Now()
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() {
					defer close(done)
					store.runRetentionSweeps(ctx, func(err error) { t.Errorf("worker: %v", err) })
				}()
				defer func() { cancel(); <-done }()
				synctest.Wait()
				if conflicts > 0 {
					for i := 1; i < conflicts; i++ {
						inject = true
						time.Sleep(time.Second)
						synctest.Wait()
						if inject {
							publish()
							inject = false
						}
					}
					time.Sleep(time.Second)
					synctest.Wait()
					runtime.ReadMemStats(&after)
					t.Logf("four-second workload: plans=%d worker_alloc_bytes=%d", plans, after.TotalAlloc-before.TotalAlloc-publicationAlloc)
					if plans != 1 {
						t.Fatalf("publication burst caused %d plans, want one delayed age attempt", plans)
					}
					if _, err := os.Stat(filepath.Join(store.directory, "retention-sweep")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("stale plan published sweep marker: %v", err)
					}
					for i := range 8 {
						if _, err := os.Stat(filepath.Join(store.directory, replayRecordName("/w", fmt.Sprint(i), false))); err != nil {
							t.Fatal(err)
						}
					}
					time.Sleep(time.Minute - time.Since(started))
					synctest.Wait()
				}
				cancel()
				<-done
				runtime.ReadMemStats(&after)
				allocated := after.TotalAlloc - before.TotalAlloc - publicationAlloc
				t.Logf("worker: publications=%d plans=%d worker_alloc_bytes=%d virtual_elapsed=%s", publications, plans, allocated, time.Since(started))
				wantPlans := 1
				if conflicts > 0 {
					wantPlans++
				}
				// Reuse the per-plan allowance above, including cold decoder
				// buffers and race instrumentation; count plans independently.
				budget := uint64(wantPlans) * (32 << 20)
				if plans != wantPlans || allocated > budget {
					t.Fatalf("worker budget: plans=%d want=%d allocated=%d budget=%d", plans, wantPlans, allocated, budget)
				}
				if _, err := os.Stat(filepath.Join(store.directory, "retention-sweep")); err != nil {
					t.Fatal(err)
				}
				for i := range 8 {
					retentionTestExists(t, store, "/w", fmt.Sprint(i), false)
				}
				for i := range conflicts {
					retentionTestExists(t, peer, "/independent", fmt.Sprint(i), true)
				}
			})
		})
	}
}
