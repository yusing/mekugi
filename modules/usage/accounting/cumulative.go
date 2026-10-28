package accounting

import (
	"slices"
	"strings"
)

// ObserveCumulative admits an already-priced native lifetime snapshot. The
// caller excludes fork ancestry and supplies one uninterrupted cumulative
// counter domain, not SDK invocation totals that reset between requests.
func (u *Service) ObserveCumulative(thread string, report Report) {
	if u == nil || thread == "" || len(thread) > maxThreadIdentityBytes {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return
	}
	u.loadLocked(thread)
	total := u.threads[thread]
	changed := total == nil
	if total == nil {
		total = newThreadUsageTotal()
	}
	delta := newThreadUsageTotal()
	delta.priorUnknown = report.PriorUnknown || report.Incomplete
	delta.counts.Inconsistent = report.Inconsistent
	delta.cost.Known = report.Cost.Known
	for _, pair := range []struct {
		reported, known uint64
		dst             *uint64
	}{
		{report.InputTokens, total.counts.InputTokens, &delta.counts.InputTokens},
		{report.UncachedInputTokens, total.counts.UncachedInputTokens, &delta.counts.UncachedInputTokens},
		{report.CacheWriteTokens, total.counts.CacheWriteTokens, &delta.counts.CacheWriteTokens},
		{report.OutputTokens, total.counts.OutputTokens, &delta.counts.OutputTokens},
		{report.ReasoningTokens, total.counts.ReasoningTokens, &delta.counts.ReasoningTokens},
		{report.MissingUsage, total.missingUsage, &delta.missingUsage},
		{report.Roundtrips, total.roundtrips, &delta.roundtrips},
	} {
		if pair.reported < pair.known {
			delta.priorUnknown = true
			continue
		}
		*pair.dst = pair.reported - pair.known
		changed = changed || *pair.dst != 0
	}
	for _, pair := range []struct {
		reported, known float64
		dst             *float64
	}{
		{report.Cost.UncachedInput, total.cost.UncachedInput, &delta.cost.UncachedInput},
		{report.Cost.CachedInput, total.cost.CachedInput, &delta.cost.CachedInput},
		{report.Cost.Output, total.cost.Output, &delta.cost.Output},
		{report.Cost.Reported, total.cost.Reported, &delta.cost.Reported},
	} {
		if pair.reported < pair.known {
			delta.priorUnknown = true
			continue
		}
		*pair.dst = pair.reported - pair.known
		changed = changed || *pair.dst != 0
	}
	for model := range strings.SplitSeq(report.Model, ",") {
		model = strings.TrimSpace(model)
		if model != "" && !slices.Contains(total.models, model) {
			delta.models = append(delta.models, model)
			changed = true
		}
	}
	changed = changed || delta.priorUnknown && !total.priorUnknown ||
		delta.counts.Inconsistent && !total.counts.Inconsistent ||
		!delta.cost.Known && total.cost.Known
	if changed {
		// Cumulative counters carry no paired request timing. Preserve any
		// existing throughput, but never infer a sample from the aggregate.
		u.updateLocked(thread, delta)
	}
}
