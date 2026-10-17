package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation/composer"
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
func showRunningProcesses(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, owned *map[string]bool, files *detailFiles) {
	deleteDraftRange(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil, (*owned), 0, len((*editor).Text))

	terminal.Selection = nil
	files.openDetailSelection(&attached.session, attached.presentation.View().Version, activitySource(attached, terminal, terminal.Agents), terminal, terminal.Agents, nil, true)
}
