package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/rollout"
)

// Restore only observational context; a live observation always wins.
func restoreContextUsage(agent *sessionpresentation.ActivityAgent, info binding.ThreadInfo) {
	if agent == nil || agent.ContextKnown {
		return
	}
	if usage, known := rollout.ReadContextUsage(info.ID, info.Path); known {
		agent.ContextTokens, agent.ContextWindow, agent.ContextKnown = usage.Tokens, usage.Window, true
	}
}
