package history

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
)

func pruneFixture(tb testing.TB, owners, records, recordBytes int, extraKinds ...recordkind.Spec) (*Store, *int) {
	tb.Helper()
	decoded := new(int)
	kinds := []recordkind.Spec{
		{ID: "entry", Match: func(name string) bool { return strings.HasPrefix(name, "entry-") }, Dependencies: func(recordkind.Record) ([]string, error) {
			*decoded += 1
			return []string{"payload-shared"}, nil
		}},
		{ID: "payload", Match: func(name string) bool { return name == "payload-shared" }},
	}
	service, err := NewService(append(kinds, extraKinds...))
	if err != nil {
		tb.Fatal(err)
	}
	store := service.Attach(tb.TempDir())
	if err := store.Transaction(tb.Context(), nil, func(tx *Transaction) error {
		if err := tx.Write("payload-shared", "payload-pending-", []byte("shared bytes")); err != nil {
			return err
		}
		names := make([]string, records)
		for i := range records {
			names[i] = fmt.Sprintf("entry-%04d", i)
			if err := tx.Write(names[i], "entry-pending-", bytes.Repeat([]byte("x"), recordBytes)); err != nil {
				return err
			}
		}
		for i := range owners {
			if err := tx.Retain(fmt.Sprint("owner-", i), Limits{}, names...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	*decoded = 0
	return store, decoded
}

func TestPruneBoundedBatchesPreserveDependenciesAndCatalogUntilRestart(t *testing.T) {
	store, _ := pruneFixture(t, 1, 35, 1)
	ageRetentionCatalog(t, store, "owner-0")
	m := &maintenance{Store: store}
	plan, err := m.planStoragePrune(t.Context())
	if err != nil || plan == nil || len(plan.snapshot.sessions) != 1 {
		t.Fatalf("plan=%v err=%v", plan, err)
	}
	candidate := &plan.snapshot.sessions[0]
	path := filepath.Join(store.directory, SessionName("owner-0"))
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	committed, _, err := m.commitStoragePrune(t.Context(), plan, candidate)
	if err != nil || !committed || len(candidate.files) != 36-16 {
		t.Fatalf("first batch: committed=%v remaining=%d err=%v", committed, len(candidate.files), err)
	}
	current, err := os.Stat(path)
	if err != nil || !os.SameFile(original, current) {
		t.Fatalf("partial batch rewrote catalog: %v", err)
	}
	requireRetentionFiles(t, store.directory, true, "payload-shared", "entry-0016")
	restarted := store.service.Attach(store.directory)
	if err := restarted.Cleanup(t.Context(), nil, Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	for i := range 35 {
		requireRetentionFiles(t, store.directory, false, fmt.Sprintf("entry-%04d", i))
	}
	requireRetentionFiles(t, store.directory, false, "payload-shared", SessionName("owner-0"))
}

func TestPruneCatalogHasNoPerBatchWriteAmplification(t *testing.T) {
	const records = 1024
	store, _ := pruneFixture(t, 1, records, 1)
	m := &maintenance{Store: store}
	plan, err := m.planStoragePrune(t.Context())
	if err != nil || plan == nil || len(plan.snapshot.sessions) != 1 {
		t.Fatalf("plan=%v err=%v", plan, err)
	}
	path := filepath.Join(store.directory, SessionName("owner-0"))
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	candidate := &plan.snapshot.sessions[0]
	wantFreed := original.Size() + records + int64(len("shared bytes"))
	var freed int64
	batches := 0
	for len(candidate.files) != 0 {
		before := len(candidate.files)
		committed, reclaimed, err := m.commitStoragePrune(t.Context(), plan, candidate)
		if err != nil || !committed || before-len(candidate.files) != min(16, before) {
			t.Fatalf("batch: committed=%v removed=%d err=%v", committed, before-len(candidate.files), err)
		}
		freed += reclaimed
		batches++
		current, err := os.Stat(path)
		if len(candidate.files) == 0 {
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final catalog removal: %v", err)
			}
		} else if err != nil || !os.SameFile(original, current) {
			t.Fatalf("batch %d rewrote catalog: %v", batches, err)
		}
	}
	if batches != (records+1+15)/16 || freed != wantFreed {
		t.Fatalf("batches=%d freed=%d wantFreed=%d", batches, freed, wantFreed)
	}
}

func TestPruneScanDecodesSharedRecordsOnceAndRejectsStaleRevision(t *testing.T) {
	store, decoded := pruneFixture(t, 4, 8, 1<<20)
	m := &maintenance{Store: store}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	plan, err := m.planStoragePrune(t.Context())
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("planning: allocated=%d elapsed=%s", allocated, time.Since(started))
	if err != nil || plan == nil || len(plan.snapshot.sessions) != 4 || *decoded != 8 {
		t.Fatalf("plan=%v decoded=%d err=%v", plan != nil, *decoded, err)
	}
	if allocated > 32<<20 {
		t.Fatalf("planning allocated %d bytes, budget %d", allocated, 32<<20)
	}
	for i := range 8 {
		if owners := plan.snapshot.owners[fmt.Sprintf("entry-%04d", i)]; owners != 4 {
			t.Fatalf("entry %d owners=%d", i, owners)
		}
	}
	peer := store.service.Attach(store.directory)
	if err := peer.Transaction(t.Context(), nil, func(tx *Transaction) error {
		return tx.Write("entry-peer", "entry-pending-", []byte("peer publication"))
	}); err != nil {
		t.Fatal(err)
	}
	committed, _, err := m.commitStoragePrune(t.Context(), plan, &plan.snapshot.sessions[0])
	if committed || !errors.Is(err, errStoragePruneStale) {
		t.Fatalf("stale commit: committed=%v err=%v", committed, err)
	}
	for i := range 8 {
		requireRetentionFiles(t, store.directory, true, fmt.Sprintf("entry-%04d", i))
	}
}

func BenchmarkPruneScanProgress(b *testing.B) {
	for _, owners := range []int{1, 4} {
		b.Run(fmt.Sprint("owners=", owners), func(b *testing.B) {
			store, _ := pruneFixture(b, owners, 8, 1<<20)
			m := &maintenance{Store: store}
			b.ReportAllocs()
			for b.Loop() {
				if plan, err := m.planStoragePrune(b.Context()); err != nil || plan == nil {
					b.Fatalf("plan: %v", err)
				}
			}
		})
	}
}

func countRetirementPlanner(count int) recordkind.RetirementPlanner {
	return func(_ context.Context, retirement recordkind.Retirement) (*recordkind.RetirementUpdate, error) {
		next := count + len(retirement.Deleted)
		return &recordkind.RetirementUpdate{Data: []byte(strconv.Itoa(next)), Next: countRetirementPlanner(next)}, nil
	}
}

func retirementPlanFixture(t *testing.T) (*maintenance, *storagePrunePlan, *storageCandidate) {
	t.Helper()
	store, _ := pruneFixture(t, 1, 17, 1, recordkind.Spec{ID: "index", Match: func(name string) bool { return strings.HasPrefix(name, "index-") }, Keep: true, Retirement: func(_ context.Context, record recordkind.Record) (recordkind.RetirementPlanner, error) {
		count, err := strconv.Atoi(string(record.Data))
		return countRetirementPlanner(count), err
	}})
	if err := store.Transaction(t.Context(), nil, func(tx *Transaction) error {
		for _, name := range []string{"index-a", "index-z"} {
			if err := tx.Write(name, "index-pending-", []byte("0")); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := &maintenance{Store: store}
	plan, err := m.planStoragePrune(t.Context())
	if err != nil || plan == nil || len(plan.snapshot.sessions) != 1 {
		t.Fatalf("plan=%v err=%v", plan, err)
	}
	return m, plan, &plan.snapshot.sessions[0]
}

func TestPruneRejectedSnapshotLeasePreservesPlannerForRetry(t *testing.T) {
	m, plan, candidate := retirementPlanFixture(t)
	release, err := m.SnapshotLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	committed, _, err := m.commitStoragePrune(t.Context(), plan, candidate)
	if committed || !errors.Is(err, errStoragePruneBusy) {
		t.Fatalf("leased commit: committed=%v err=%v", committed, err)
	}
	data, err := os.ReadFile(filepath.Join(m.directory, "index-a"))
	if err != nil || string(data) != "0" || len(candidate.files) != 18 {
		t.Fatalf("rejected effects: index=%s remaining=%d err=%v", data, len(candidate.files), err)
	}
	release()
	for _, want := range []string{"16", "18"} {
		committed, _, err := m.commitStoragePrune(t.Context(), plan, candidate)
		if err != nil || !committed {
			t.Fatalf("retry commit: committed=%v err=%v", committed, err)
		}
		data, err := os.ReadFile(filepath.Join(m.directory, "index-a"))
		if err != nil || string(data) != want {
			t.Fatalf("planner advancement: data=%s want=%s err=%v", data, want, err)
		}
	}
}

func TestPrunePartialUpdatesAdvanceOnlyPublishedPlanner(t *testing.T) {
	m, plan, candidate := retirementPlanFixture(t)
	// Inject a publication failure after planning, without a new revision, so
	// index-a publishes before index-z encounters a non-regular target.
	path := filepath.Join(m.directory, "index-z")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	committed, _, err := m.commitStoragePrune(t.Context(), plan, candidate)
	if committed || err == nil {
		t.Fatalf("partial commit: committed=%v err=%v", committed, err)
	}
	for name, want := range map[string]string{"index-a": "16", "index-z": "0"} {
		update, err := plan.retirement[name](t.Context(), recordkind.Retirement{Deleted: map[string]bool{}})
		if err != nil || update == nil || string(update.Data) != want {
			t.Fatalf("%s planner advanced without publication: update=%v err=%v", name, update, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(m.directory, "index-a"))
	if err != nil || string(data) != "16" {
		t.Fatalf("first update unpublished: data=%s err=%v", data, err)
	}
}
