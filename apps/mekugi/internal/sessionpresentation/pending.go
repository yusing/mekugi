package sessionpresentation

import (
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

// Source: apps/mekugi/internal/router/app_server_input_queue.go:330:355@[cf6c7832]
func pendingInput(control *sessioncontrol.Live, conversation *conversationmodel.Session) conversationmodel.PendingView {
	ids := slices.Clone(control.Steers())
	if submission := control.Submission(); submission.Turn != "" {
		ids = append(ids, submission.ID)
	}
	reason := conversationmodel.Waiting
	switch {
	case control.Compaction.PreservesWaitingInput():
		reason = conversationmodel.CompactAfterInterrupt
	case control.Shell.Standalone():
		reason = conversationmodel.ShellRunning
	case control.SendSteersAfterInterrupt || control.SteerInterruptAckPending:
		reason = conversationmodel.SendingAfterInterrupt
	case control.Interruption.Target != "":
		reason = conversationmodel.RestoringAfterInterrupt
	case control.Compaction.Active():
		reason = conversationmodel.CompactRunning
	case len(conversation.Unsent) > 0 && conversation.Unsent[0].Compact != nil:
		reason = conversationmodel.CompactQueued
	case control.Turn != "" || control.Starting():
		reason = conversationmodel.Steering
	case control.Settings.Pending() || control.Settings.RestoreEffort:
		reason = conversationmodel.ApplyingSettings
	}
	return conversation.Pending(ids, reason)
}
