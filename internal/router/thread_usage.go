package router

import (
	"cmp"
	"slices"
	"strings"
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
	models   []string
}

type threadUsageObservation struct {
	once          sync.Once
	totals        *threadUsage
	conflicted    bool
	model         string
	openCodePrice *openCodePrice
	serviceTier   string
	thread        string
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
		o.totals.add(o.thread, o.model, cmp.Or(counts.ServiceTier, o.serviceTier), counts, o.conflicted, o.openCodePrice)
	})
}

// A forwarded request without terminal usage leaves a permanent accounting gap.
// A later successful response must not revive an apparently complete total.
func (o *threadUsageObservation) finish() {
	o.observe(tokenCounts{Incomplete: true})
}

func (u *threadUsage) add(thread, model, serviceTier string, counts tokenCounts, conflicted bool, price *openCodePrice) {
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
		total = &threadUsageTotal{complete: true, cost: tokenCost{known: true}}
		u.threads[thread] = total
	}
	displayModel := model
	if model != "" && (serviceTier == "fast" || serviceTier == "priority") {
		displayModel += " fast"
	}
	if displayModel != "" && !slices.Contains(total.models, displayModel) {
		total.models = append(total.models, displayModel)
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
	cost := estimateTokenCost(model, serviceTier, counts)
	if isOpenCodeModel(model) {
		cost = tokenCost{}
		if price != nil {
			cost = price.estimate(serviceTier, counts)
		}
	}
	total.cost.add(cost)
	total.counts = sum
}

func (u *threadUsage) snapshot(thread string) (tokenUsageReport, bool) {
	if u == nil {
		return tokenUsageReport{}, false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if total := u.threads[thread]; !u.closed && total != nil && total.complete {
		return tokenUsageReport{tokenCounts: total.counts, cost: total.cost, model: strings.Join(total.models, ", ")}, true
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

// completionUsageReport consolidates only proven descendants. Usage itself stays
// keyed by transport thread, independent of presentation names and routing sessions.
func (t *mekugiResponseTransform) completionUsageReport() (tokenUsageReport, bool) {
	if t.subagentTurn || t.usageTracker == nil {
		return tokenUsageReport{}, false
	}
	report, observed := t.threadUsageCounts()
	if !observed {
		return report, false
	}
	rows := []agentTokenUsage{{agent: "/root", role: "main", report: report}}
	if t.proxy != nil && t.journalAvailable {
		journals := t.proxy.journals
		release, err := journals.lockState(t.ctx)
		if err != nil {
			return tokenUsageReport{}, false
		}
		var children []threadJournal
		read := func() error {
			var err error
			children, err = journals.descendants(t.proxy.replayStore, t.directory, t.usageTracker.thread)
			return err
		}
		if t.proxy.replayStore != nil {
			err = t.proxy.replayStore.locked(t.ctx, read)
		} else {
			err = read()
		}
		release()
		if err != nil {
			return tokenUsageReport{}, false
		}
		for _, child := range children {
			counts, ok := t.usageTracker.totals.snapshot(child.Thread)
			if !ok {
				counts.Incomplete = true
			}
			rows = append(rows, agentTokenUsage{agent: child.Author, role: "n/a", report: counts})
			report.cost.add(counts.cost)
			if !ok || !addTokenUsageCounts(&report.tokenCounts, counts.tokenCounts) {
				report.Incomplete = true
				report.cost.known = false
			}
		}
	}
	report.model = ""
	report.rows = &rows
	return report, true
}

func addTokenUsageCounts(sum *tokenCounts, next tokenCounts) bool {
	for _, pair := range []struct {
		dst *uint64
		add uint64
	}{
		{&sum.InputTokens, next.InputTokens},
		{&sum.UncachedInputTokens, next.UncachedInputTokens},
		{&sum.CacheWriteTokens, next.CacheWriteTokens},
		{&sum.OutputTokens, next.OutputTokens},
		{&sum.ReasoningTokens, next.ReasoningTokens},
	} {
		if ^uint64(0)-*pair.dst < pair.add {
			return false
		}
		*pair.dst += pair.add
	}
	sum.Inconsistent = sum.Inconsistent || next.Inconsistent
	return true
}
