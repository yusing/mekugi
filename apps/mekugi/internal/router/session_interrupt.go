package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

func (u *appServerUI) interruptSession(intent sessionpresentation.InterruptIntent) error {
	defer u.settlePresentation()
	unsent := u.attached.conversation.Unsent
	decision, err := u.attached.presentation.Interrupt(intent, &u.Live, len(unsent) > 0 && unsent[0].Text != "/compact")
	if err != nil {
		u.setNotice(err.Error(), true)
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
	u.applyInputRestoration(u.attached.presentation.RestoreInterruptedInput(u.attached.conversation, intent))
	if intent.Turn == "" {
		return nil
	}
	u.endSyncQuestions(intent.Turn)
	_, err := u.Requests.Send("turn/interrupt", map[string]any{"threadId": u.Thread, "turnId": intent.Turn}, "main", appServerRequest{label: intent.Label})
	if err != nil && intent.Label == "steer/interrupt" {
		u.SteerInterruptAckPending = false
	}
	return err
}
