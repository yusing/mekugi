package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

func runningProcessCount(session *sessionpresentation.Attachment) int {
	count := 0
	for range session.RunningProcesses() {
		count++
	}
	return count
}

// showRunningProcesses opens the background list backed by live command details.
func (session *nativeSession) showRunningProcesses(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, files *detailFiles) {
	session.deleteDraftRange(attached, picker, editor, (*side), clock, session.overlayOpen(*panel, *resumePicker), 0, len((*editor).Text))

	files.openDetailSelection(&attached.session, attached.presentation.View().Version, attached.childSource, nil, true)
}
