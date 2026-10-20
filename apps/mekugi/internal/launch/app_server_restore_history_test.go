package launch

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
	restoreMainHistory(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.statusPanel, &u.resumePicker, u.ctx, u.review, &u.pendingEditReceipts, u.notifications, u.retained, u.retainedSession, u.activity, u.segments, &u.restoredSegments, turns, nil, nil)
}
