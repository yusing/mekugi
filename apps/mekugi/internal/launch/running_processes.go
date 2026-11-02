package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func runningProcessCount(session *sessionpresentation.Attachment) int {
	count := 0
	for range session.RunningProcesses() {
		count++
	}
	return count
}

// showRunningProcesses opens the shared output dialog on every live command.
func (session *nativeSession) showRunningProcesses(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, files *detailFiles) {
	session.deleteDraftRange(attached, terminal, picker, editor, (*side), clock, session.overlayOpen(*panel, *resumePicker), 0, len((*editor).Text))

	terminal.Selection = nil
	files.openDetailSelection(&attached.session, attached.presentation.View().Version, activitySource(attached, terminal, terminal.Agents), terminal, terminal.Agents, nil, true)
}
