package accounting

import "github.com/yusing/mekugi/contracts/inference"

type inputSample struct {
	model, tier string
	counts      inference.Usage
	price       func(string, inference.Usage) Cost
}

// CompareInputCost prices a proposed uncached input against the last observed
// request's input, preserving its actual cache split and pinned provider rates.
// Missing, stale-model/tier and restored-only evidence is unavailable, not zero.
func (u *Service) CompareInputCost(thread, model, tier string, tokens uint64) (current, cold Cost, known bool) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	total := u.threads[thread]
	if total == nil || total.input == nil || u.closed {
		return
	}
	sample := total.input
	if sample.model != model || standardTier(sample.tier) != standardTier(tier) {
		return
	}
	price := sample.price
	if price == nil {
		price = func(tier string, counts inference.Usage) Cost { return estimateReferenceCost(model, tier, counts) }
	}
	counts := sample.counts
	counts.OutputTokens, counts.ReasoningTokens = 0, 0
	current = price(sample.tier, counts)
	cold = price(sample.tier, inference.Usage{InputTokens: tokens, UncachedInputTokens: tokens})
	return current, cold, current.Known && cold.Known
}

func standardTier(tier string) string {
	if tier == "default" {
		return ""
	}
	return tier
}
