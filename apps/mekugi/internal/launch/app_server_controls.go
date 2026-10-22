package launch

import (
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func controlsCommand(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, owned *map[string]bool, text string) (bool, error) {
	if text == "/predictions" || strings.HasPrefix(text, "/predictions ") {
		parts := strings.Fields(text)
		if len(parts) != 2 || parts[1] != "on" && parts[1] != "off" {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Usage: /predictions on|off", true)
			return true, nil
		}
		terminal.Main.PredictionsDisabled = parts[1] == "off"
		attached.conversation.Prediction = conversationmodel.Prediction{}
		deleteDraftRange(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay, (*owned), 0, len((*editor).Text))
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Predictions "+parts[1], false)
		return true, nil
	}
	switch text {
	case "/lock", "/unlock":
		terminal.Main.InterruptLocked = text == "/lock"
		deleteDraftRange(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay, (*owned), 0, len((*editor).Text))
		if terminal.Main.InterruptLocked {
			lockNotice(attached, terminal, clock)
		} else {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Unlocked · Esc and Ctrl-C can interrupt", false)
		}
		return true, nil
	}
	return false, nil
}

func lockNotice(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time) {
	setNotice(&attached.feedback, terminal, presentationTime(clock), "Locked · /unlock restores Esc and Ctrl-C · /quit exits when idle", false)
}
