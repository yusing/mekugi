package router

// restoreHistory hydrates Main from host turns alone, without rollout evidence.
func (u *appServerUI) restoreHistory(turns []appServerHistoryTurn) {
	u.restoreMainHistory(turns, nil, nil)
}
