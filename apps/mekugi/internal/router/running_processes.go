package router

func (u *appServerUI) runningProcessCount() int {
	count := 0
	for range u.session.RunningProcesses() {
		count++
	}
	return count
}

// showRunningProcesses opens the shared output dialog on every live command.
func (u *appServerUI) showRunningProcesses() {
	u.deleteDraftRange(0, len(u.Text))
	u.ensureShell()
	u.shell.Selection = nil
	u.detailFiles.openDetailSelection(&u.session, u.sessionPresentation().View().Version, u.activitySource(u.agents), u.shell, u.agents, nil, true)
}
