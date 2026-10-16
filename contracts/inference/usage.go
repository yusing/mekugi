package inference

// Usage carries one provider-authoritative terminal observation. Per-thread
// accounting and invocation-wide diagnostics consume the same measured facts.
type Usage struct {
	// TotalsKnown distinguishes reported zero totals from absent totals.
	TotalsKnown         bool
	InputTokens         uint64
	UncachedInputTokens uint64
	CacheWriteTokens    uint64
	OutputTokens        uint64
	ReasoningTokens     uint64

	// Preserve inconsistent raw categories before normalizing input for existing consumers.
	Inconsistent bool
	// Missing categories remain zero for existing normalized capture consumers,
	// but cannot establish complete thread totals or a reference cost.
	Incomplete bool
	// The terminal provider tier overrides the requested tier for pricing only.
	ServiceTier string
}
