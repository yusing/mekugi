package router

import (
	"fmt"
	"strconv"
	"strings"
)

// tokenCost contains estimates for disjoint billable categories. Reasoning is
// already included in output; cached input is already included in input.
type tokenCost struct {
	uncachedInput float64
	cachedInput   float64
	output        float64
	known         bool
}

type tokenUsageReport struct {
	tokenCounts
	cost tokenCost
}

type tokenPrice struct {
	input, cachedInput, output             float64
	longInput, longCachedInput, longOutput float64
}

// Source: session-usage/scripts/session_usage.py:625:655 select_tier and cost_for_request.
// Rates mirror FALLBACK_USD_PER_MILLION in the same script. These are reference
// list API prices in USD per million tokens, not subscription charges or live billing quotes.
var tokenReferencePrices = map[string]tokenPrice{
	"gpt-6-astra":     {10, 1, 50, 20, 2, 75},
	"gpt-6-astra-pro": {10, 1, 50, 20, 2, 75},
	"gpt-5.5":         {5, 0.5, 30, 10, 1, 45},
	"gpt-5.6-sol":     {4, 0.4, 20, 8, 0.8, 30},
	"gpt-5.6-terra":   {2, 0.2, 12, 4, 0.4, 18},
	"gpt-5.6-luna":    {0.2, 0.02, 1.2, 0.4, 0.04, 1.8},
	"gpt-5.4":         {2.5, 0.25, 15, 5, 0.5, 22.5},
	"gpt-5.4-mini":    {0.75, 0.075, 4.5, 0, 0, 0},
	"gpt-5.4-nano":    {0.2, 0.02, 1.25, 0, 0, 0},
}

// Fast and priority are aliases. These are model-specific rates, not a blanket
// multiplier: GPT-5.5 has a different premium and no published long-context rate.
// Source (2026-09-12): https://developers.openai.com/api/docs/pricing#text-tokens
var tokenFastReferencePrices = map[string]tokenPrice{
	"gpt-6-astra":   {20, 2, 100, 40, 4, 150},
	"gpt-5.6-sol":   {8, 0.8, 40, 16, 1.6, 60},
	"gpt-5.6-terra": {4, 0.4, 24, 8, 0.8, 36},
	"gpt-5.6-luna":  {0.4, 0.04, 2.4, 0.8, 0.08, 3.6},
	"gpt-5.5":       {12.5, 1.25, 75, 0, 0, 0},
	"gpt-5.4":       {5, 0.5, 30, 0, 0, 0},
	"gpt-5.4-mini":  {1.5, 0.15, 9, 0, 0, 0},
}

func estimateTokenCost(model, serviceTier string, counts tokenCounts) tokenCost {
	model = strings.TrimPrefix(model, "openai/")
	price, ok := tokenReferencePrices[model]
	switch serviceTier {
	case "", "default":
	case "priority", "fast":
		price, ok = tokenFastReferencePrices[model]
		if counts.InputTokens > 272_000 && price.longInput == 0 && tokenReferencePrices[model].longInput != 0 {
			return tokenCost{}
		}
	default:
		return tokenCost{}
	}
	if !ok || counts.Incomplete || counts.Inconsistent || counts.UncachedInputTokens > counts.InputTokens || counts.ReasoningTokens > counts.OutputTokens || counts.CacheWriteTokens > counts.UncachedInputTokens {
		return tokenCost{}
	}
	// Select the tier per response, never from cumulative thread input.
	if counts.InputTokens > 272_000 && price.longInput != 0 {
		price.input, price.cachedInput, price.output = price.longInput, price.longCachedInput, price.longOutput
	}
	var cacheWritePremium float64
	if counts.CacheWriteTokens != 0 {
		switch model {
		case "gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
			// Writes are part of uncached input, charged at 1.25x its rate.
			cacheWritePremium = float64(counts.CacheWriteTokens) * price.input * .25 / 1_000_000
		default:
			return tokenCost{}
		}
	}
	return tokenCost{
		uncachedInput: float64(counts.UncachedInputTokens)*price.input/1_000_000 + cacheWritePremium,
		cachedInput:   float64(counts.InputTokens-counts.UncachedInputTokens) * price.cachedInput / 1_000_000,
		output:        float64(counts.OutputTokens) * price.output / 1_000_000,
		known:         true,
	}
}

func (cost *tokenCost) add(next tokenCost) {
	cost.uncachedInput += next.uncachedInput
	cost.cachedInput += next.cachedInput
	cost.output += next.output
	cost.known = cost.known && next.known
}

func formatTokenUsageReport(report tokenUsageReport) string {
	var text strings.Builder
	text.WriteString("Tokens:\n\n| Category | Tokens | API USD |\n| --- | ---: | ---: |\n")
	for _, row := range []struct {
		label  string
		tokens string
		usd    float64
		billed bool
	}{
		{"Input", formatUsageTokens(report.InputTokens), 0, false},
		{"Cached input", formatUsageTokens(report.InputTokens - min(report.InputTokens, report.UncachedInputTokens)), report.cost.cachedInput, true},
		{"Uncached input", formatUsageTokens(report.UncachedInputTokens), report.cost.uncachedInput, true},
		{"Output", formatUsageTokens(report.OutputTokens), report.cost.output, true},
		{"Reasoning", formatUsageTokens(report.ReasoningTokens), 0, false},
		{"Total", "—", report.cost.uncachedInput + report.cost.cachedInput + report.cost.output, true},
	} {
		amount := "—"
		if row.billed {
			amount = "n/a"
			if report.cost.known {
				amount = fmt.Sprintf("$%.4f", row.usd)
			}
		}
		fmt.Fprintf(&text, "| %s | %s | %s |\n", row.label, row.tokens, amount)
	}
	text.WriteString("\nThis thread since router start; cached input and reasoning are included, not added. Reference API prices, not subscription charges.")
	if report.CacheWriteTokens != 0 {
		fmt.Fprintf(&text, " Uncached input includes %s cache-write tokens, priced at the cache-write rate.", formatUsageTokens(report.CacheWriteTokens))
	}
	if !report.cost.known {
		text.WriteString(" Cost unavailable: unknown model/service-tier pricing or inconsistent usage.")
	}
	return text.String()
}

func formatUsageTokens(count uint64) string {
	digits := strconv.FormatUint(count, 10)
	var text strings.Builder
	for i := range len(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			text.WriteByte(',')
		}
		text.WriteByte(digits[i])
	}
	return text.String()
}
