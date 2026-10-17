package router

import "github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

func runningProcessCount(session *sessionpresentation.Attachment) int {
	count := 0
	for range session.RunningProcesses() {
		count++
	}
	return count
}

// showRunningProcesses opens the shared output dialog on every live command.
func (u *appServerUI) showRunningProcesses() {
	u.deleteDraftRange(0, len(u.Text))

	u.shell.Selection = nil
	u.detailFiles.openDetailSelection(&u.attached.session, u.attached.presentation.View().Version, activitySource(&u.attached, u.shell, u.shell.Agents), u.shell, u.shell.Agents, nil, true)
}
