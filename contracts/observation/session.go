package observation

// SessionMetrics are current runtime observations, not cumulative provider usage.
type SessionMetrics struct {
	ContextTokens *uint64 `json:"context_tokens"`
	ContextWindow *uint64 `json:"context_window"`
	Compactions   *uint64 `json:"compactions"`
}

type SessionDetails struct {
	Model           *string `json:"model"`
	ReasoningEffort *string `json:"reasoning_effort"`
	ServiceTier     *string `json:"service_tier"`
	SessionMetrics
}
