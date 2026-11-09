package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func (session *nativeSession) shouldReviewHooks() bool {
	bypass, _ := session.Client.Startup.Config["bypass_hook_trust"].(bool)
	return session.reviewHooks && session.Client.Startup.Prepared == nil && !bypass
}

func (session *nativeSession) beginHookTrust() error {
	session.Client.HookReview = true
	_, err := session.Client.Requests.Send("hooks/list", map[string]any{"threadId": session.Client.Thread}, "", appServerRequest{label: "hooks/trust/list"})
	return err
}

func (session *nativeSession) hookTrustResponse(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, approval *sessioncontrol.Approval, message appserver.Message) error {
	if approval != nil {
		approval.Hook.Saving = false
		if message.Error != nil {
			setNotice(&attached.feedback, presentationTime(clock), "Could not save hook trust: "+message.Error.Message, true)
			return nil
		}
		endApproval(attached, &session.Client, picker, editor, &session.questions, &session.approvals, clock, session.notifications, session.review, &session.pendingEditReceipts, approval, "Trusted")
	} else if message.Error != nil {
		setNotice(&attached.feedback, presentationTime(clock), "Could not check hook trust: "+message.Error.Message, true)
	} else {
		approvals, err := sessioncontrol.HookTrustApprovals(message.Result, attached.cwd)
		if err != nil {
			setNotice(&attached.feedback, presentationTime(clock), "Could not check hook trust: "+err.Error(), true)
		} else {
			for _, a := range approvals {
				a.Thread = session.Client.Thread
				addApproval(attached, &session.Client, picker, editor, &session.questions, &session.approvals, clock, false, session.notifications, session.review, &session.pendingEditReceipts, a)
			}
		}
	}
	session.continueAfterHookTrust()
	return nil
}

func (session *nativeSession) continueAfterHookTrust() {
	for _, a := range session.approvals.calls() {
		if a.Hook != nil {
			return
		}
	}
	session.Client.HookReview = false
}
