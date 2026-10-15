package router

import "github.com/yusing/mekugi/backends/codex/rollout"

// Restore only observational context; a live observation always wins.
func restoreContextUsage(agent *activityPaneAgent, info appServerThreadInfo) {
	if agent == nil || agent.ContextKnown {
		return
	}
	if usage, known := rollout.ReadContextUsage(info.ID, info.Path); known {
		agent.ContextTokens, agent.ContextWindow, agent.ContextKnown = usage.Tokens, usage.Window, true
	}
}

type hostContextUsage struct {
	tokens, window uint64
}

// Live and restored host usage share reminder and subslice-reset policy.
func (p *mekugiProxy) observeContextSliceUsage(thread string, known bool, used, window uint64) {
	if p == nil || thread == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !known {
		delete(p.contextUsage, thread)
		return
	}
	if p.contextUsage == nil {
		p.contextUsage = make(map[string]hostContextUsage)
	}
	p.contextUsage[thread] = hostContextUsage{tokens: used, window: window}
}

func (u *appServerUI) restoreContextUsage(agent *activityPaneAgent, info appServerThreadInfo) {
	if info.ID == u.thread {
		u.session.compactionRollout = appServerThreadInfo{ID: info.ID, Path: info.Path, Cwd: info.Cwd}
	}
	restoreContextUsage(agent, info)
	if agent != nil {
		u.proxy.observeContextSliceUsage(info.ID, agent.ContextKnown, agent.ContextTokens, agent.ContextWindow)
	}
}
