package accounting

import (
	"math"
	"strings"

	"github.com/yusing/mekugi/contracts/inference"
)

// Cost contains already-priced estimates. Reported holds native estimates that
// do not provide a category breakdown. Reasoning is already included in output;
// cached input is already included in input.
type Cost struct {
	UncachedInput float64
	CachedInput   float64
	Output        float64
	Reported      float64
	Known         bool
}

func (cost Cost) Total() float64 {
	return cost.UncachedInput + cost.CachedInput + cost.Output + cost.Reported
}

type Report struct {
	inference.Usage
	Cost         Cost
	Model        string
	MissingUsage uint64
	PriorUnknown bool
	Roundtrips   uint64
	RoundOutput  RoundOutput
}

type tokenPrice struct {
	input, cachedInput, output             float64
	longInput, longCachedInput, longOutput float64
}

// Source: session-usage/scripts/session_usage.py:625:655 select_tier and cost_for_request.
// OpenAI rates mirror FALLBACK_USD_PER_MILLION in the same script. Grok rates
// follow the xAI model pricing pages (2026-09-23), including Build Fast tiers.
// These are reference list API prices in USD per million tokens, not subscription
// charges or live billing quotes. OpenCode rates are refreshed separately from
// its online catalog; these OpenAI/xAI fallback rates are not used for OpenCode.
// GPT-6.1 Sol source (2026-09-30): https://developers.openai.com/api/docs/pricing
// GPT-6.1 Terra source (2026-10-27): https://developers.openai.com/api/docs/models/gpt-6.1-terra
var tokenReferencePrices = map[string]tokenPrice{
	"gpt-6-astra":         {10, 1, 50, 20, 2, 75},
	"gpt-6-astra-pro":     {10, 1, 50, 20, 2, 75},
	"gpt-6.1-sol":         {2, 0.1, 10, 4, 0.2, 15},
	"gpt-6.1-terra":       {1, 0.05, 5, 2, 0.1, 7.5},
	"gpt-6-sol":           {2, 0.2, 10, 4, 0.4, 15},
	"gpt-6-luna":          {0.1, 0.01, 0.5, 0.2, 0.02, 0.75},
	"gpt-5.5":             {5, 0.5, 30, 10, 1, 45},
	"gpt-5.6-sol":         {4, 0.4, 20, 8, 0.8, 30},
	"gpt-5.6-terra":       {2, 0.2, 12, 4, 0.4, 18},
	"gpt-5.6-luna":        {0.2, 0.02, 1.2, 0.4, 0.04, 1.8},
	"gpt-5.4":             {2.5, 0.25, 15, 5, 0.5, 22.5},
	"gpt-5.4-mini":        {0.75, 0.075, 4.5, 0, 0, 0},
	"gpt-5.4-nano":        {0.2, 0.02, 1.25, 0, 0, 0},
	"grok-4.6":            {2, 0.5, 6, 4, 1, 12},
	"grok-4.5":            {2, 0.3, 6, 4, 0.6, 12},
	"grok-4.7":            {2, 0.5, 6, 4, 1, 12},
	"grok-4.7-build-fast": {4, 1, 12, 6, 1.5, 18},
}

// Fast and priority are aliases. These are model-specific rates, not a blanket
// multiplier: GPT-5.5 has a different premium and no published long-context rate.
// Source (2026-09-12): https://developers.openai.com/api/docs/pricing#text-tokens
var tokenFastReferencePrices = map[string]tokenPrice{
	"gpt-6-astra":   {20, 2, 100, 40, 4, 150},
	"gpt-6.1-sol":   {4, 0.2, 20, 8, 0.4, 30},
	"gpt-6.1-terra": {2, 0.1, 10, 4, 0.2, 15},
	"gpt-6-sol":     {4, 0.4, 20, 8, 0.8, 30},
	"gpt-6-luna":    {0.2, 0.02, 1, 0.4, 0.04, 1.5},
	"gpt-5.6-sol":   {8, 0.8, 40, 16, 1.6, 60},
	"gpt-5.6-terra": {4, 0.4, 24, 8, 0.8, 36},
	"gpt-5.6-luna":  {0.4, 0.04, 2.4, 0.8, 0.08, 3.6},
	"gpt-5.5":       {12.5, 1.25, 75, 0, 0, 0},
	"gpt-5.4":       {5, 0.5, 30, 0, 0, 0},
	"gpt-5.4-mini":  {1.5, 0.15, 9, 0, 0, 0},
}

func tokenCostModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimPrefix(model, "openai/")
	return strings.TrimPrefix(model, "grok:")
}

func tokenLongContextApplies(model string, price tokenPrice, input uint64) bool {
	if price.longInput == 0 {
		return false
	}
	after := uint64(272_000)
	if strings.HasPrefix(model, "grok-4.") {
		after = 199_999
		if model == "grok-4.7-build-fast" {
			after = 200_000
		}
	}
	return input > after
}

func validCostCounts(counts inference.Usage) bool {
	return !counts.Incomplete && !counts.Inconsistent &&
		counts.UncachedInputTokens <= counts.InputTokens &&
		counts.ReasoningTokens <= counts.OutputTokens &&
		counts.CacheWriteTokens <= counts.UncachedInputTokens
}

func estimateReferenceCost(model, serviceTier string, counts inference.Usage) Cost {
	model = tokenCostModel(model)
	price, ok := tokenReferencePrices[model]
	standard := price
	switch serviceTier {
	case "", "default":
	case "priority", "fast":
		price, ok = tokenFastReferencePrices[model]
		if tokenLongContextApplies(model, standard, counts.InputTokens) && price.longInput == 0 {
			return Cost{}
		}
	default:
		return Cost{}
	}
	if !ok || !validCostCounts(counts) {
		return Cost{}
	}
	// Select the tier per response, never from cumulative thread input.
	if tokenLongContextApplies(model, price, counts.InputTokens) {
		price.input, price.cachedInput, price.output = price.longInput, price.longCachedInput, price.longOutput
	}
	var cacheWritePremium float64
	if counts.CacheWriteTokens != 0 {
		switch model {
		case "gpt-6-astra", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
			// Writes are part of uncached input, charged at 1.25x its rate.
			cacheWritePremium = float64(counts.CacheWriteTokens) * price.input * .25 / 1_000_000
		default:
			return Cost{}
		}
	}
	return Cost{
		UncachedInput: float64(counts.UncachedInputTokens)*price.input/1_000_000 + cacheWritePremium,
		CachedInput:   float64(counts.InputTokens-counts.UncachedInputTokens) * price.cachedInput / 1_000_000,
		Output:        float64(counts.OutputTokens) * price.output / 1_000_000,
		Known:         true,
	}
}

func (cost *Cost) add(next Cost) {
	cost.UncachedInput += next.UncachedInput
	cost.CachedInput += next.CachedInput
	cost.Output += next.Output
	cost.Reported += next.Reported
	cost.Known = cost.Known && next.Known
}

// Rates are selected, immutable USD-per-million-token prices. Nil means unknown.
type Rates struct{ Input, Output, CacheRead, CacheWrite *float64 }

func EstimateRates(price Rates, tier string, counts inference.Usage) Cost {
	if (tier != "" && tier != "default") || !validCostCounts(counts) {
		return Cost{}
	}
	valid := func(value *float64) bool {
		return value != nil && *value >= 0 && !math.IsNaN(*value) && !math.IsInf(*value, 0)
	}
	if !valid(price.Input) || !valid(price.Output) {
		return Cost{}
	}
	result := Cost{
		UncachedInput: float64(counts.UncachedInputTokens-counts.CacheWriteTokens) * *price.Input / 1e6,
		Output:        float64(counts.OutputTokens) * *price.Output / 1e6,
		Known:         true,
	}
	if cached := counts.InputTokens - counts.UncachedInputTokens; cached != 0 {
		if !valid(price.CacheRead) {
			return Cost{}
		}
		result.CachedInput = float64(cached) * *price.CacheRead / 1e6
	}
	if counts.CacheWriteTokens != 0 {
		if !valid(price.CacheWrite) {
			return Cost{}
		}
		result.UncachedInput += float64(counts.CacheWriteTokens) * *price.CacheWrite / 1e6
	}
	return result
}
