package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func interruptSession(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, intent sessionpresentation.InterruptIntent) error {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay)
	}()
	unsent := attached.conversation.Unsent
	active, err := attached.presentation.ResolveInterrupt(intent, &control.Live, &attached.session)
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return nil
	}
	if !active {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Original work already ended.", false)
		return nil
	}
	decision := control.RequestInterrupt(intent.Mode, len(unsent) > 0 && unsent[0].Compact == nil)
	err = applyInterruption(attached, terminal, control, picker, editor, questions, approvals, clock, reviewOwner, receipts, decision)
	if err != nil && intent.Mode == sessioncontrol.ExpediteSteers {
		control.SendSteersAfterInterrupt = false
	}
	return err
}

// applyInterruption applies the owner's composer consequence and issues at most
// one native interruption. Host execution remains independent of presentation.
func applyInterruption(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, intent sessioncontrol.Interrupt) error {
	attached.threadInput.reset()
	applyInputRestoration(attached, terminal, questions, approvals, picker, control.Turn, clock, attached.presentation.RestoreInterruptedInput(attached.conversation, intent))
	if intent.Stop != "" && attached.interruptWait != nil {
		attached.interruptWait(control.Thread, intent.Wait)
		attached.presentation.Status.SetText("Interrupting…")
		if intent.Turn == "" {
			return nil
		}
	}
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
