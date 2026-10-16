package router

import (
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/modules/usage/accounting"
	"github.com/yusing/mekugi/providers/opencode"
)

// Provider-specific context-tier selection uses the exchange's pinned catalog.
func estimateOpenCodeCost(price opencode.Price, tier string, counts inference.Usage) accounting.Cost {
	selected, ok := price.ForInput(counts.InputTokens)
	if !ok {
		return accounting.Cost{}
	}
	return accounting.EstimateRates(accounting.Rates{Input: selected.Input, Output: selected.Output, CacheRead: selected.CacheRead, CacheWrite: selected.CacheWrite}, tier, counts)
}

func pinnedOpenCodePrice(price *opencode.Price) func(string, inference.Usage) accounting.Cost {
	if price == nil {
		return nil
	}
	pinned := *price
	return func(tier string, counts inference.Usage) accounting.Cost {
		return estimateOpenCodeCost(pinned, tier, counts)
	}
}
