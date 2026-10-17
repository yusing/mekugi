package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
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
	err = applyInterruption(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, u.review, &u.pendingEditReceipts, decision)
	if err != nil && intent.Mode == sessioncontrol.ExpediteSteers {
		u.SendSteersAfterInterrupt = false
	}
	return err
}

// applyInterruption applies the owner's composer consequence and issues at most
// one native interruption. Host execution remains independent of presentation.
func applyInterruption(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, intent sessioncontrol.Interrupt) error {
	applyInputRestoration(attached, terminal, questions, approvals, picker, control.Turn, clock, attached.presentation.RestoreInterruptedInput(attached.conversation, intent))
	if intent.Turn == "" {
		return nil
	}
	endSyncQuestions(attached, terminal, control, editor, questions, approvals, clock, reviewOwner, receipts, intent.Turn)
	_, err := control.Requests.Send("turn/interrupt", map[string]any{"threadId": control.Thread, "turnId": intent.Turn}, "main", appServerRequest{label: intent.Label})
	if err != nil && intent.Label == "steer/interrupt" {
		control.SteerInterruptAckPending = false
	}
	return err
}
