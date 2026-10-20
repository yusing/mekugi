package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/notifications/notice"
)

// Successful feedback is transient; actionable errors remain until editing.
func setNotice(feedback *noticepresentation.Model, terminal *terminalsession.Attachment, at time.Time, text string, alert bool) {
	feedback.Set(text, alert, at)
	terminal.Main.NoticeDetails, terminal.Main.NoticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
}

func expireNotice(feedback *noticepresentation.Model, terminal *terminalsession.Attachment, now time.Time) bool {
	if !feedback.Expire(now) {
		return false
	}
	terminal.Main.NoticeDetails, terminal.Main.NoticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
	return true
}

// Claims retain only the identity they published and their independent queues.
type nativeCriticalNoticeDelivery struct {
	attachment, feedback uint64
	errors, progress     notice.Batch
}

func (d *nativeCriticalNoticeDelivery) finish(receipt terminalsession.NoticeReceipt) {
	if d == nil {
		return
	}
	attached := receipt.Attachment == d.attachment
	d.errors.Finish(attached && receipt.Feedback == d.feedback && receipt.Composer)
	d.progress.Finish(attached && receipt.Content)
}

func applyCriticalNotices(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring **appServerActivityRestore, side **sessioncontrol.Side, audience *sessioncontrol.Audience, dirty *bool, issues *requestexecution.Failures) (delivery *nativeCriticalNoticeDelivery) {
	defer func() {
		view := publishPresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, (*restoring) != nil, (*side), presentationTime(clock))
		if delivery != nil {
			delivery.attachment, delivery.feedback = view.Version.Attachment, attached.feedback.Revision
		}
	}()
	claim := claimNativeNotices(issues, control.Thread, audience)
	if claim == nil {
		return nil
	}
	delivery = &nativeCriticalNoticeDelivery{errors: claim.Errors, progress: claim.Progress}
	for _, message := range attached.feedback.Receive(claim, presentationTime(clock)) {
		attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: attached.source.LastSeq + 1, Agent: "Main", Kind: "progress", Text: message.Text, Observed: presentationTime(clock)}}})
	}
	if len(claim.Errors.Messages) > 0 {
		terminal.Main.NoticeDetails, terminal.Main.NoticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
	}
	(*dirty) = true
	return delivery
}
