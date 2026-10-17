package router

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/rollout"
)

// Restore only observational context; a live observation always wins.
func restoreContextUsage(agent *activityPaneAgent, info binding.ThreadInfo) {
	if agent == nil || agent.ContextKnown {
		return
	}
	if usage, known := rollout.ReadContextUsage(info.ID, info.Path); known {
		agent.ContextTokens, agent.ContextWindow, agent.ContextKnown = usage.Tokens, usage.Window, true
	}
}

func (u *appServerUI) restoreContextUsage(agent *activityPaneAgent, info binding.ThreadInfo) {
	if info.ID == u.Thread {
		u.session.compactionRollout = binding.ThreadInfo{ID: info.ID, Path: info.Path, Cwd: info.Cwd}
	}
	restoreContextUsage(agent, info)
}
