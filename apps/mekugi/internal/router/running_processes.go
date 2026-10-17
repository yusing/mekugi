package router

func (u *appServerUI) runningProcessCount() int {
	count := 0
	for range u.attached.session.RunningProcesses() {
		count++
	}
	return count
}

// showRunningProcesses opens the shared output dialog on every live command.
func (u *appServerUI) showRunningProcesses() {
	u.deleteDraftRange(0, len(u.Text))

	u.shell.Selection = nil
	u.detailFiles.openDetailSelection(&u.attached.session, u.attached.presentation.View().Version, u.activitySource(u.shell.Agents), u.shell, u.shell.Agents, nil, true)
}
