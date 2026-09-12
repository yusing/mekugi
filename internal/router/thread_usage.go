package router

import (
	"cmp"
	"sync"
)

// Thread totals are auxiliary, independent of routing sessions and replay history.
// Existing identities are never evicted: an untracked thread must not later show a
// partial lifetime total as though it were complete.
type threadUsage struct {
	mu      sync.Mutex
	threads map[string]*threadUsageTotal
	closed  bool
}

type threadUsageTotal struct {
	cost     tokenCost
	counts   tokenCounts
	complete bool
}

type threadUsageObservation struct {
	once        sync.Once
	totals      *threadUsage
	conflicted  bool
	model       string
	serviceTier string
	thread      string
}

func newThreadUsage() *threadUsage {
	return &threadUsage{threads: make(map[string]*threadUsageTotal)}
}

// Transport identity owns usage accounting, not auxiliary author or ancestry
// metadata. A contradictory explicit thread ID makes lifetime totals incomplete.
func (u *threadUsage) observation(thread, metadataThread, model, serviceTier string) *threadUsageObservation {
	return &threadUsageObservation{totals: u, thread: thread, model: model, serviceTier: serviceTier, conflicted: metadataThread != "" && metadataThread != thread}
}

func (o *threadUsageObservation) observe(counts tokenCounts) {
	if o == nil {
		return
	}
	o.once.Do(func() {
		o.totals.add(o.thread, o.model, cmp.Or(counts.ServiceTier, o.serviceTier), counts, o.conflicted)
	})
}

// A forwarded request without terminal usage leaves a permanent accounting gap.
// A later successful response must not revive an apparently complete total.
func (o *threadUsageObservation) finish() {
	o.observe(tokenCounts{Incomplete: true})
}

func (u *threadUsage) add(thread, model, serviceTier string, counts tokenCounts, conflicted bool) {
	if u == nil || thread == "" || len(thread) > maxCommentaryPublicationBytes {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return
	}
	total := u.threads[thread]
	if total == nil {
		if len(u.threads) >= maxCommentaryRoutes {
			return
		}
		total = &threadUsageTotal{complete: true, cost: tokenCost{known: true}}
		u.threads[thread] = total
	}
	if conflicted || counts.Incomplete {
		total.complete = false
	}
	if !total.complete {
		return
	}
	sum := total.counts
	for _, pair := range []struct {
		dst *uint64
		add uint64
	}{
		{&sum.InputTokens, counts.InputTokens},
		{&sum.UncachedInputTokens, counts.UncachedInputTokens},
		{&sum.CacheWriteTokens, counts.CacheWriteTokens},
		{&sum.OutputTokens, counts.OutputTokens},
		{&sum.ReasoningTokens, counts.ReasoningTokens},
	} {
		if ^uint64(0)-*pair.dst < pair.add {
			total.complete = false
			return
		}
		*pair.dst += pair.add
	}
	sum.Inconsistent = sum.Inconsistent || counts.Inconsistent
	total.cost.add(estimateTokenCost(model, serviceTier, counts))
	total.counts = sum
}

func (u *threadUsage) snapshot(thread string) (tokenUsageReport, bool) {
	if u == nil {
		return tokenUsageReport{}, false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if total := u.threads[thread]; !u.closed && total != nil && total.complete {
		return tokenUsageReport{tokenCounts: total.counts, cost: total.cost}, true
	}
	return tokenUsageReport{}, false
}

func (u *threadUsage) close() {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	clear(u.threads)
}

func (t *mekugiResponseTransform) threadUsageCounts() (tokenUsageReport, bool) {
	if t.usageTracker == nil {
		return tokenUsageReport{}, false
	}
	return t.usageTracker.totals.snapshot(t.usageTracker.thread)
}
