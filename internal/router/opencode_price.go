package router

import("math";"github.com/yusing/mekugi/providers/opencode")

// Source: internal/router/opencode_catalog.go:385:418@[18475aaf] cost accounting.
func estimateOpenCodeCost(price opencode.Price, tier string, counts tokenCounts) tokenCost {
	if (tier != "" && tier != "default") || !validCostCounts(counts) {
		return tokenCost{}
	}
	var priceOK bool
	price, priceOK = price.ForInput(counts.InputTokens)
	if !priceOK {
		return tokenCost{}
	}
	valid := func(value *float64) bool {
		return value != nil && *value >= 0 && !math.IsNaN(*value) && !math.IsInf(*value, 0)
	}
	if !valid(price.Input) || !valid(price.Output) {
		return tokenCost{}
	}
	result := tokenCost{
		uncachedInput: float64(counts.UncachedInputTokens-counts.CacheWriteTokens) * *price.Input / 1e6,
		output:        float64(counts.OutputTokens) * *price.Output / 1e6,
		known:         true,
	}
	if cached := counts.InputTokens - counts.UncachedInputTokens; cached != 0 {
		if !valid(price.CacheRead) {
			return tokenCost{}
		}
		result.cachedInput = float64(cached) * *price.CacheRead / 1e6
	}
	if counts.CacheWriteTokens != 0 {
		if !valid(price.CacheWrite) {
			return tokenCost{}
		}
		result.uncachedInput += float64(counts.CacheWriteTokens) * *price.CacheWrite / 1e6
	}
	return result
}
