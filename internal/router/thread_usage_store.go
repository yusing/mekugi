package router

import (
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	RoundOutput                          providerRoundOutput
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
		complete: r.Complete, priorUnknown: r.PriorUnknown, missingUsage: r.MissingUsage, roundtrips: r.Roundtrips, models: r.Models, roundOutput: r.RoundOutput}, nil
}

func writeThreadUsage(store *mekugiReplayStore, thread string, total *threadUsageTotal) error {
	r := threadUsageRecord{Version: 1, Thread: thread, Counts: total.counts,
		UncachedCost: total.cost.uncachedInput, CachedCost: total.cost.cachedInput, OutputCost: total.cost.output, CostKnown: total.cost.known,
		Complete: total.complete, PriorUnknown: total.priorUnknown, MissingUsage: total.missingUsage, Roundtrips: total.roundtrips, Models: total.models, RoundOutput: total.roundOutput}
	data, err := json.Marshal(&r)
	if err != nil {
		return err
	}
	return store.writeManagedFile(threadUsageName(thread), "usage-pending-", data)
}

func newThreadUsageTotal() *threadUsageTotal {
	return &threadUsageTotal{complete: true, cost: tokenCost{known: true}}
}

type threadUsageWrite struct {
	baseline threadUsageTotal
	delta    *threadUsageTotal
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

// Atomic publication makes a single record safe to read without store.lock.
// Absence, including concurrent retirement, is an unknown prior window rather
// than a new lifetime. Cache it so UI refreshes do no further disk I/O.
func (u *threadUsage) loadLocked(thread string) {
	if _, loaded := u.threads[thread]; loaded || u.store == nil || thread == "" {
		return
	}
	total, err := readThreadUsage(u.store, thread)
	if err != nil {
		u.storageFailure(thread, err)
		return
	}
	if total != nil {
		// Retained counters prove consumption, not uninterrupted coverage.
		total.priorUnknown = true
	}
	u.threads[thread] = total
}

// Merge already-priced observations, never reprice an aggregate at its final
// model, tier or input size. Pending memory is one total per thread, not a queue
// growing with every response while storage is busy.
func mergeThreadUsageTotal(total, delta *threadUsageTotal) {
	if delta.roundOutput.StartedUnixNano > 0 && delta.roundOutput.StartedUnixNano >= total.roundOutput.StartedUnixNano {
		total.roundOutput.StartedUnixNano = delta.roundOutput.StartedUnixNano
		total.outputEstimate = 0
		if _, known := delta.roundOutput.Throughput.Rate(); known {
			total.roundOutput.Throughput = delta.roundOutput.Throughput
		}
	}
	total.priorUnknown = total.priorUnknown || delta.priorUnknown
	for _, model := range delta.models {
		if !slices.Contains(total.models, model) {
			total.models = append(total.models, model)
		}
	}
	if ^uint64(0)-total.roundtrips < delta.roundtrips {
		total.complete = false
		return
	}
	total.roundtrips += delta.roundtrips
	total.complete = total.complete && delta.complete
	if !total.complete {
		return
	}
	sum := total.counts
	for _, pair := range []struct {
		dst *uint64
		add uint64
	}{
		{&sum.InputTokens, delta.counts.InputTokens},
		{&sum.UncachedInputTokens, delta.counts.UncachedInputTokens},
		{&sum.CacheWriteTokens, delta.counts.CacheWriteTokens},
		{&sum.OutputTokens, delta.counts.OutputTokens},
		{&sum.ReasoningTokens, delta.counts.ReasoningTokens},
	} {
		if ^uint64(0)-*pair.dst < pair.add {
			total.complete = false
			return
		}
		*pair.dst += pair.add
	}
	if ^uint64(0)-total.missingUsage < delta.missingUsage {
		total.complete = false
		return
	}
	total.missingUsage += delta.missingUsage
	sum.Inconsistent = sum.Inconsistent || delta.counts.Inconsistent
	total.counts = sum
	total.cost.add(delta.cost)
}

func (u *threadUsage) updateLocked(thread string, delta *threadUsageTotal) {
	u.loadLocked(thread)
	total := u.threads[thread]
	if total == nil {
		total = newThreadUsageTotal()
		total.priorUnknown = u.store != nil && !u.fresh[thread]
		u.threads[thread] = total
	}
	delta.priorUnknown = delta.priorUnknown || total.priorUnknown
	var baseline threadUsageTotal
	if u.store != nil && !u.storageFailed[thread] && u.pending[thread] == nil {
		baseline = *total
		baseline.models = slices.Clone(total.models)
	}
	mergeThreadUsageTotal(total, delta)
	if u.store == nil || u.storageFailed[thread] {
		return
	}
	if u.pending == nil {
		u.pending = make(map[string]*threadUsageWrite)
	}
	if pending := u.pending[thread]; pending != nil {
		mergeThreadUsageTotal(pending.delta, delta)
	} else {
		u.pending[thread] = &threadUsageWrite{baseline: baseline, delta: delta}
	}
	if u.writerDone == nil {
		ctx, cancel := context.WithCancel(context.Background())
		u.writerCancel, u.writerDone = cancel, make(chan struct{})
		go u.runWriter(ctx)
	}
	if !u.writing {
		u.changedLocked()
	}
}

func (u *threadUsage) changedLocked() {
	close(u.changed)
	u.changed = make(chan struct{})
}

// flush waits for managed publication without holding the accounting mutex.
// Canceling a waiter does not cancel or discard the writer's pending facts.
func (u *threadUsage) flush(ctx context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	for len(u.pending) != 0 || u.writing {
		changed := u.changed
		u.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			u.mu.Lock()
			return ctx.Err()
		}
		u.mu.Lock()
	}
	return nil
}

func (u *threadUsage) runWriter(ctx context.Context) {
	defer close(u.writerDone)
	// Collect bursts before taking the store lock. Pending state remains one delta
	// per thread; flush and shutdown still wait for publication of all observations.
	coalesce := func() {
		timer := time.NewTimer(25 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	coalesce()
	for {
		u.mu.Lock()
		if err := ctx.Err(); err != nil {
			for thread := range u.pending {
				u.storageFailure(thread, err)
			}
			clear(u.pending)
			u.changedLocked()
			u.mu.Unlock()
			return
		}
		var thread string
		var write *threadUsageWrite
		for thread, write = range u.pending {
			delete(u.pending, thread)
			break
		}
		if write == nil {
			changed := u.changed
			u.mu.Unlock()
			select {
			case <-changed:
			case <-ctx.Done():
			}
			coalesce()
			continue
		}
		u.writing = true
		u.mu.Unlock()

		retained, err := u.persist(ctx, thread, write)
		u.mu.Lock()
		if err != nil {
			// A write or unlock failure may follow publication. Never replay
			// the same delta, or overwrite another writer with our live cache.
			u.storageFailure(thread, err)
			delete(u.pending, thread)
		} else {
			// Include concurrent local observations and other router writers.
			if pending := u.pending[thread]; pending != nil {
				pending.baseline = *retained
				pending.baseline.models = slices.Clone(retained.models)
				mergeThreadUsageTotal(retained, pending.delta)
			}
			if live := u.threads[thread]; live != nil && live.roundOutput.StartedUnixNano == retained.roundOutput.StartedUnixNano {
				retained.outputEstimate = live.outputEstimate
			}
			u.threads[thread] = retained
		}
		u.writing = false
		u.changedLocked()
		u.mu.Unlock()
	}
}

// Only this background writer waits on the publication lock. Normal contention
// is not a storage failure and cannot stall provider delivery or roster reads.
func (u *threadUsage) persist(ctx context.Context, thread string, write *threadUsageWrite) (*threadUsageTotal, error) {
	store := *u.store
	store.session = storageSessionIdentity{Thread: thread, Namespace: thread}
	var retained *threadUsageTotal
	err := store.locked(ctx, func() error {
		var err error
		retained, err = readThreadUsage(&store, thread)
		if err != nil {
			return err
		}
		if retained == nil {
			// Retirement must not discard consumption still known in this
			// live owner. The baseline excludes this batch and later deltas.
			retained = &write.baseline
		}
		mergeThreadUsageTotal(retained, write.delta)
		return writeThreadUsage(&store, thread, retained)
	})
	return retained, err
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
		delta := newThreadUsageTotal()
		delta.priorUnknown = true
		u.updateLocked(thread, delta)
	}
}
