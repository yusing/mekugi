package launch

import (
	"github.com/yusing/mekugi/backends/codex/binding"
)

func (u *appServerUI) restoreHistory(turns []binding.HistoryTurn) {
	u.nativeSession.restoreMainHistory(&u.attached, u.shell, &u.picker, &u.Editor, u.clock, &u.statusPanel, &u.resumePicker, u.ctx, turns, nil, nil)
}
