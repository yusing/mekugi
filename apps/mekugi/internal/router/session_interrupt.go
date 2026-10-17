package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func (u *appServerUI) interruptSession(intent sessionpresentation.InterruptIntent) error {
	defer func() {
		settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	}()
	unsent := u.attached.conversation.Unsent
	decision, err := u.attached.presentation.Interrupt(intent, &u.Live, len(unsent) > 0 && unsent[0].Text != "/compact")
	if err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
		return nil
	}
	err = u.applyInterruption(decision)
	if err != nil && intent.Mode == sessioncontrol.ExpediteSteers {
		u.SendSteersAfterInterrupt = false
	}
	return err
}

// interruptTurn applies the owner's composer consequence and issues at most
// one native interruption. Host execution remains independent of presentation.
func (u *appServerUI) interruptTurn() error {
	return u.applyInterruption(u.Interrupt())
}

func (u *appServerUI) applyInterruption(intent sessioncontrol.Interrupt) error {
	applyInputRestoration(&u.attached, u.shell, &u.questions, &u.approvals, &u.picker, u.Turn, u.clock, u.attached.presentation.RestoreInterruptedInput(u.attached.conversation, intent))
	if intent.Turn == "" {
		return nil
	}
	endSyncQuestions(&u.attached, u.shell, &u.Client, &u.Editor, &u.questions, &u.approvals, u.clock, u.review, &u.pendingEditReceipts, intent.Turn)
	_, err := u.Requests.Send("turn/interrupt", map[string]any{"threadId": u.Thread, "turnId": intent.Turn}, "main", appServerRequest{label: intent.Label})
	if err != nil && intent.Label == "steer/interrupt" {
		u.SteerInterruptAckPending = false
	}
	return err
}
