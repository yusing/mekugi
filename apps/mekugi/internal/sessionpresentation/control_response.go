package sessionpresentation

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

const ResumeSettingsUnavailable = "Saved model settings unavailable; using Codex defaults and explicit flags"

func (m *Model) BeginOpening(opening sessioncontrol.Opening) {
	switch opening {
	case sessioncontrol.OpeningFresh:
		m.Status.SetText("Starting thread…")
	case sessioncontrol.OpeningResume:
		m.Status.SetText("Resuming thread…")
	case sessioncontrol.OpeningLatest:
		m.Status.SetText("Finding latest thread…")
	case sessioncontrol.OpeningPicker:
		m.Status.SetText("Choose a session")
	}
}

// AcknowledgeCompaction restores the command before later waiting input on
// rejection. Host acknowledgement and compact-turn completion remain separate.
// Source: apps/mekugi/internal/router/app_server_ui.go:610:619@a4869fd4.
func (m *Model) AcknowledgeCompaction(control *sessioncontrol.Live, conversation *conversationmodel.Session, failure string, failed bool) InputEffects {
	control.CompactResponse(failed)
	var effects InputEffects
	if failed {
		parts := append([]conversationmodel.Input{{Text: "/compact"}}, conversation.TakeWaiting()...)
		effects.Restored = conversation.RestoreInput(parts...)
		effects.Notice, effects.NoticeError = "Compaction failed: "+failure, true
		m.Status.SetText("Ready")
	}
	return effects
}

// AcknowledgeInterrupt applies the control disposition after either ordering of
// rejection and turn completion, without resending uncommitted steers.
// Source: apps/mekugi/internal/router/app_server_ui.go:620:647,663:667@a4869fd4.
func (m *Model) AcknowledgeInterrupt(control *sessioncontrol.Live, conversation *conversationmodel.Session, method, failure string, failed bool) InputEffects {
	var effects InputEffects
	if control.InterruptResponse(method, failed) {
		effects.Restored = conversation.RestoreInput(conversation.TakeWaiting()...)
	}
	if !failed {
		return effects
	}
	switch method {
	case "compact/interrupt":
		effects.Notice = "Could not interrupt for compaction: " + failure
		m.Status.SetText("Ready")
		if control.Turn != "" {
			m.Status.SetText("Working")
		}
	case "steer/interrupt":
		effects.Notice = "Could not interrupt to send steer: " + failure
	default:
		m.Status.Set(method+": "+failure, true)
	}
	effects.NoticeError = true
	return effects
}

// SettingsAcknowledged applies a settled control result to ordinary input and
// user feedback. A failed default-effort repair keeps its independent gate.
// Source: apps/mekugi/internal/router/app_server_settings.go:250:284@a4869fd4.
func (m *Model) SettingsAcknowledged(conversation *conversationmodel.Session, method, failure string, ack sessioncontrol.SettingsAcknowledgement) InputEffects {
	var effects InputEffects
	if ack.Failed {
		effects.Notice, effects.NoticeError = method+": "+failure, true
		if ack.RestoreWaiting {
			effects.Restored = conversation.RestoreInput(conversation.TakeWaiting()...)
			effects.Notice = "Could not restore default reasoning: " + failure + " · choose /effort VALUE or restart resume"
		}
	} else if method == "turn/settings/update" {
		switch ack.Status {
		case "applied":
			effects.Notice = "Settings saved · live update published for subsequent steps"
		case "targetUnavailable":
			effects.Notice = "Settings saved for next turn · live target finished"
		default:
			effects.Notice, effects.NoticeError = "Settings saved · unknown live update status: "+ack.Status, true
		}
	}
	return effects
}
