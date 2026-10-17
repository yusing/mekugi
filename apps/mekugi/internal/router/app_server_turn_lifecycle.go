package router

// Native turn completion retires display state even when interrupted tools
// never return. It neither finalizes capture nor stops a host-owned process.
func (u *appServerUI) endTurnPreviews(thread, turn string) {
	u.execWindows.StopTurnPreviews(thread, turn)
	if live := u.review.Preview(); live != nil {
		live.SetTurn(thread, turn, false)
		live.FinishTurn(u.cwd, thread, turn)
	}
	if u.shell != nil {
		u.shell.Live.EndTurn(thread, turn, u.now())
	}
}
