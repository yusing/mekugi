package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/rollout"
)

// A snapshot can seed a view but must not replace a newer live measurement.
func applyContextUsage(agent *sessionpresentation.ActivityAgent, info binding.ThreadInfo) {
	if agent != nil && !agent.ContextKnown && info.ContextTokens != nil {
		agent.ContextTokens, agent.ContextWindow, agent.ContextKnown = *info.ContextTokens, nativeValue(info.ContextWindow), true
	}
}

// Retained-only inspection can open an unloaded thread whose metadata has no
// context snapshot. Never open its rollout on a live admission.
func restoreContextUsage(agent *sessionpresentation.ActivityAgent, info binding.ThreadInfo) {
	if agent == nil || agent.ContextKnown {
		return
	}
	if usage, known := rollout.ReadContextUsage(info.ID, info.Path); known {
		agent.ContextTokens, agent.ContextWindow, agent.ContextKnown = usage.Tokens, usage.Window, true
	}
}
