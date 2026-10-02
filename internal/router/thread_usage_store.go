package router

import (
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// This is the durable form of the canonical counters, not a second usage
// calculation. Costs have already been priced per response and must not be
// recomputed with the model or tier selected at resume time.
type threadUsageRecord struct {
	Version                              int
	Thread                               string
	Counts                               tokenCounts
	UncachedCost, CachedCost, OutputCost float64
	CostKnown, Complete, PriorUnknown    bool
	MissingUsage, Roundtrips             uint64
	Models                               []string
}

func threadUsageName(thread string) string {
	return fmt.Sprintf("usage-%x.json", sha256.Sum256([]byte(thread)))
}

func readThreadUsage(store *mekugiReplayStore, thread string) (*threadUsageTotal, error) {
	data, err := readManagedOutputFile(filepath.Join(store.directory, threadUsageName(thread)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r threadUsageRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Version != 1 || r.Thread != thread || r.MissingUsage > r.Roundtrips ||
		r.UncachedCost < 0 || r.CachedCost < 0 || r.OutputCost < 0 {
		return nil, errors.New("invalid provider usage record")
	}
	return &threadUsageTotal{counts: r.Counts, cost: tokenCost{uncachedInput: r.UncachedCost, cachedInput: r.CachedCost, output: r.OutputCost, known: r.CostKnown},
		complete: r.Complete, priorUnknown: r.PriorUnknown, missingUsage: r.MissingUsage, roundtrips: r.Roundtrips, models: r.Models}, nil
}

func writeThreadUsage(store *mekugiReplayStore, thread string, total *threadUsageTotal) error {
	r := threadUsageRecord{Version: 1, Thread: thread, Counts: total.counts,
		UncachedCost: total.cost.uncachedInput, CachedCost: total.cost.cachedInput, OutputCost: total.cost.output, CostKnown: total.cost.known,
		Complete: total.complete, PriorUnknown: total.priorUnknown, MissingUsage: total.missingUsage, Roundtrips: total.roundtrips, Models: total.models}
	data, err := json.Marshal(&r)
	if err != nil {
		return err
	}
	return store.writeManagedFile(threadUsageName(thread), "usage-pending-", data)
}

// Usage is auxiliary: storage contention has a bounded wait, independent of
// request cancellation, and cannot fail or replace the provider response.
func (u *threadUsage) storage(thread string, run func(*mekugiReplayStore) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	store := *u.store
	store.session = storageSessionIdentity{Thread: thread, Namespace: thread}
	return store.locked(ctx, func() error { return run(&store) })
}

func newThreadUsageTotal() *threadUsageTotal {
	return &threadUsageTotal{complete: true, cost: tokenCost{known: true}}
}

func (u *threadUsage) storageFailure(thread string, err error) {
	if u.storageFailed == nil {
		u.storageFailed = make(map[string]bool)
	}
	u.storageFailed[thread] = true
	if u.threads[thread] == nil {
		u.threads[thread] = newThreadUsageTotal()
	}
	u.threads[thread].priorUnknown = true
	if u.notice != nil {
		u.notice(thread, err)
	}
}

// Cache restored records, including absence, so UI refreshes do no disk I/O.
func (u *threadUsage) loadLocked(thread string) {
	if _, loaded := u.threads[thread]; loaded || u.store == nil || thread == "" {
		return
	}
	err := u.storage(thread, func(store *mekugiReplayStore) error {
		total, err := readThreadUsage(store, thread)
		if err == nil {
			if total != nil {
				// A retained counter proves observed consumption, not that every
				// later request was persisted before another router stopped.
				total.priorUnknown = true
			}
			u.threads[thread] = total
		}
		return err
	})
	if err != nil {
		u.storageFailure(thread, err)
	}
}

// Serialize read-modify-write across router processes. After an ambiguous write
// failure keep this router's live evidence, but never overwrite a durable record
// with a cache that may have missed another writer or double-add an observation.
func (u *threadUsage) updateLocked(thread string, mutate func(*threadUsageTotal)) {
	total := u.threads[thread]
	applied := false
	apply := func() {
		if total == nil {
			total = newThreadUsageTotal()
			// Accounting may precede asynchronous history hydration, and
			// headless callers never hydrate native history at all.
			total.priorUnknown = u.store != nil && !u.fresh[thread]
		}
		mutate(total)
		u.threads[thread] = total
		applied = true
	}
	if u.store == nil || u.storageFailed[thread] {
		apply()
		return
	}
	err := u.storage(thread, func(store *mekugiReplayStore) error {
		retained, err := readThreadUsage(store, thread)
		if err != nil {
			return err
		}
		if retained != nil {
			retained.priorUnknown = retained.priorUnknown || total == nil || total.priorUnknown
			total = retained
		}
		apply()
		return writeThreadUsage(store, thread, total)
	})
	if err != nil {
		if !applied {
			apply()
		}
		u.storageFailure(thread, err)
	}
}

// Only an observed native creation, not absent history or a new routing session,
// proves a fresh lifetime baseline. Late evidence is deliberately conservative:
// it must not clear an uncertainty already attached to observed consumption.
func (u *threadUsage) markNew(thread string) {
	if u == nil || thread == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed || u.threads[thread] != nil {
		return
	}
	if u.fresh == nil {
		u.fresh = make(map[string]bool)
	}
	u.fresh[thread] = true
}

// A legacy/restored thread without retained accounting has an unknown earlier
// window, not zero consumption. Never copy a fork source or host context totals.
func (u *threadUsage) restore(thread string, history bool) {
	if u == nil || thread == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return
	}
	u.loadLocked(thread)
	if history && !u.fresh[thread] && u.threads[thread] == nil {
		u.updateLocked(thread, func(total *threadUsageTotal) {
			if total.roundtrips == 0 {
				total.priorUnknown = true
			}
		})
	}
}
