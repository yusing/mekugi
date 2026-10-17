package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func runningProcessCount(session *sessionpresentation.Attachment) int {
	count := 0
	for range session.RunningProcesses() {
		count++
	}
	return count
}

// showRunningProcesses opens the shared output dialog on every live command.
func (u *appServerUI) showRunningProcesses() {
	deleteDraftRange(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, 0, len(u.Text))

	u.shell.Selection = nil
	u.detailFiles.openDetailSelection(&u.attached.session, u.attached.presentation.View().Version, activitySource(&u.attached, u.shell, u.shell.Agents), u.shell, u.shell.Agents, nil, true)
}
