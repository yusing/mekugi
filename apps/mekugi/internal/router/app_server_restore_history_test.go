package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func restoredDiscoveryTestState(root string, children ...string) *sessionpresentation.Restoration {
	state := sessionpresentation.NewRestoration(root)
	state.Discover(children, "")
	state.EndDiscovery()
	return state
}

// restoreHistory hydrates Main from host turns alone, without rollout evidence.
func (u *appServerUI) restoreHistory(turns []binding.HistoryTurn) {
	u.restoreMainHistory(turns, nil, nil)
}
