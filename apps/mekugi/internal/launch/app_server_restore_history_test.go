package launch

import (
	"github.com/yusing/mekugi/backends/codex/binding"
)

func (u *appServerUI) restoreHistory(turns []binding.HistoryTurn) {
	restoreMainHistory(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.statusPanel, &u.resumePicker, u.ctx, u.review, &u.pendingEditReceipts, u.notifications, u.retained, u.retainedSession, u.activity, u.segments, &u.restoredSegments, turns, nil, nil)
}
