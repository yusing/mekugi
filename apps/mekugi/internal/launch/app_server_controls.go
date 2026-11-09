package launch

import (
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func (session *nativeSession) controlsCommand(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, overlay bool, text string) (bool, error) {
	if text == "/predictions" || strings.HasPrefix(text, "/predictions ") {
		parts := strings.Fields(text)
		if len(parts) != 2 || parts[1] != "on" && parts[1] != "off" {
			setNotice(&attached.feedback, presentationTime(clock), "Usage: /predictions on|off", true)
			return true, nil
		}
		attached.predictionsDisabled = parts[1] == "off"
		attached.conversation.Prediction = conversationmodel.Prediction{}
		session.deleteDraftRange(attached, picker, editor, (*side), clock, overlay, 0, len((*editor).Text))
		setNotice(&attached.feedback, presentationTime(clock), "Predictions "+parts[1], false)
		return true, nil
	}
	switch text {
	case "/lock", "/unlock":
		attached.interruptLocked = text == "/lock"
		session.deleteDraftRange(attached, picker, editor, (*side), clock, overlay, 0, len((*editor).Text))
		if attached.interruptLocked {
			lockNotice(attached, clock)
		} else {
			setNotice(&attached.feedback, presentationTime(clock), "Unlocked · Esc and Ctrl-C can interrupt", false)
		}
		return true, nil
	}
	return false, nil
}

func lockNotice(attached *attachedPresentation, clock func() time.Time) {
	setNotice(&attached.feedback, presentationTime(clock), "Locked · /unlock restores Esc and Ctrl-C · /quit exits when idle", false)
}
