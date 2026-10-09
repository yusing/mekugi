package claude

import "github.com/yusing/mekugi/internal/session"

type nativeContextUsage struct {
	Type       string               `json:"type"`
	Iterations []nativeContextUsage `json:"iterations"`
	Input      *uint64              `json:"input_tokens"`
	Output     *uint64              `json:"output_tokens"`
	CacheRead  *uint64              `json:"cache_read_input_tokens"`
	CacheWrite *uint64              `json:"cache_creation_input_tokens"`
}

func (u nativeContextUsage) tokens() *uint64 {
	if len(u.Iterations) != 0 {
		for i := len(u.Iterations) - 1; i >= 0; i-- {
			iteration := u.Iterations[i]
			if iteration.Type == "advisor_message" || iteration.Type == "compaction" {
				continue
			}
			if iteration.Type != "message" && iteration.Type != "fallback_message" {
				return nil
			}
			return iteration.tokens()
		}
		return nil
	}
	if u.Input == nil || u.Output == nil {
		return nil
	}
	var total uint64
	for _, count := range []*uint64{u.Input, u.Output, u.CacheRead, u.CacheWrite} {
		if count == nil {
			continue
		}
		if ^uint64(0)-total < *count {
			return nil
		}
		total += *count
	}
	return &total
}

func (a *adapter) startContext(e nativeEvent) []session.Event {
	if e.Parent != "" || e.SessionID == "" {
		return nil
	}
	a.contextSession, a.contextMessage, a.contextUsage = e.SessionID, e.Event.Message.ID, e.Event.Message.Usage
	return []session.Event{{Kind: "context_usage", SessionID: e.SessionID, ContextTokens: a.contextUsage.tokens()}}
}

func (a *adapter) updateContext(e nativeEvent) []session.Event {
	if e.Parent != "" || e.SessionID == "" || e.SessionID != a.contextSession || a.contextMessage == "" {
		return nil
	}
	u := e.Event.Usage
	if u.Iterations != nil {
		a.contextUsage.Iterations = u.Iterations
	}
	// Server-tool loops can report cumulative inputs in message_delta. Those
	// totals do not prove the last iteration's context window.
	for _, counts := range [][2]*uint64{{a.contextUsage.Input, u.Input}, {a.contextUsage.CacheRead, u.CacheRead}, {a.contextUsage.CacheWrite, u.CacheWrite}} {
		if counts[1] != nil && (counts[0] == nil && *counts[1] != 0 || counts[0] != nil && *counts[0] != *counts[1]) {
			a.contextUsage.Input = nil
		}
	}
	if u.Output != nil {
		a.contextUsage.Output = u.Output // A cumulative snapshot within this message, not a delta.
	}
	return []session.Event{{Kind: "context_usage", SessionID: e.SessionID, ContextTokens: a.contextUsage.tokens()}}
}
