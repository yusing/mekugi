package launch

import (
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/binding"
)

// nativePublicationFacts borrows existing owners on their serialized loop. Native
// transport makes the publication detached before it reaches another goroutine.
func nativePublicationFacts(attached *attachedPresentation, panes *nativePreferences, usage binding.TokenUsage, debugDirectory string, archived bool, notifications *nativeNotifications, main string, predictions *composerPredictions) sessionapi.NativeSessionFacts {
	facts := sessionapi.NativeSessionFacts{History: attached.conversation.Editor.InputHistory, Usage: sessionapi.NativeUsage(usage), DebugDirectory: debugDirectory, Archived: archived}
	if panes != nil && panes.attachment != nil {
		facts.Preferences = panes.attachment.Snapshot().Presentation
	}
	facts.ActionRequired = attached.conversation.Questions.Count(main) > 0 || len(attached.conversation.Approvals.Pending) > 0
	if notifications != nil {
		facts.Notifications = sessionapi.NativeNotificationSettings{Ready: notifications.ready, Events: slices.Clone(notifications.settings.Events), Method: notifications.settings.Method, Condition: notifications.settings.Condition}
		facts.ActionRequired = facts.ActionRequired || len(notifications.blocked) > 0
	}
	if predictions != nil {
		facts.PredictionThread, facts.PredictionTurn = predictions.thread, predictions.turn
	}
	return facts
}
