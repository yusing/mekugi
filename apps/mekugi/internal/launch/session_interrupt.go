package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func (session *nativeSession) interruptSession(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, overlay bool, intent sessionpresentation.InterruptIntent) error {
	defer func() {
		settlePresentation(attached, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, (*side), clock, &session.questions, &session.approvals, overlay)
	}()
	if intent.Target.Thread != session.Client.Thread {
		if err := attached.presentation.ResolveSideInterrupt(intent, *side); err != nil {
			setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
			return nil
		}
		session.guardHookCheck.cancel(intent.Target.Thread)
		update, err := session.Client.Requests.InterruptSide(*side)
		applySideUpdate(attached, &session.Client, clock, update)
		return err
	}
	unsent := attached.conversation.Unsent
	active, err := attached.presentation.ResolveInterrupt(intent, &session.Client.Live, &attached.session)
	if err != nil {
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return nil
	}
	if !active {
		setNotice(&attached.feedback, presentationTime(clock), "Original work already ended.", false)
		return nil
	}
	decision := session.Client.RequestInterrupt(intent.Mode, len(unsent) > 0 && unsent[0].Compact == nil)
	if decision.RestoreWaiting {
		session.guardHookCheck.cancel(session.Client.Thread)
	}
	err = applyInterruption(attached, &session.Client, picker, editor, &session.questions, &session.approvals, clock, session.review, &session.pendingEditReceipts, decision)
	if err != nil && intent.Mode == sessioncontrol.ExpediteSteers {
		session.Client.SendSteersAfterInterrupt = false
	}
	return err
}

// applyInterruption applies the owner's composer consequence and issues at most
// one native interruption. Host execution remains independent of presentation.
func applyInterruption(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, intent sessioncontrol.Interrupt) error {
	attached.threadInput.reset()
	applyInputRestoration(attached, questions, approvals, picker, control.Turn, clock, attached.presentation.RestoreInterruptedInput(attached.conversation, intent))
	if intent.ResetRequest != "" {
		return control.Request("thread/compact/cancel", map[string]any{"threadId": control.Thread, "contextResetRequestId": intent.ResetRequest})
	}
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
	endSyncQuestions(attached, control, editor, questions, approvals, clock, reviewOwner, receipts, intent.Turn)
	_, err := control.Requests.Send("turn/interrupt", map[string]any{"threadId": control.Thread, "turnId": intent.Turn}, "main", appServerRequest{label: intent.Label})
	if err != nil && intent.Label == "steer/interrupt" {
		control.SteerInterruptAckPending = false
	}
	return err
}
