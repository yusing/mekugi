package router

import (
	"time"

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

func (u *appServerUI) applyCriticalNotices() (delivery *nativeCriticalNoticeDelivery) {
	defer func() {
		view := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
		if delivery != nil {
			delivery.attachment, delivery.feedback = view.Version.Attachment, u.attached.feedback.Revision
		}
	}()
	claim := claimNativeNotices(u.issues, u.Thread, u.audience)
	if claim == nil {
		return nil
	}
	delivery = &nativeCriticalNoticeDelivery{errors: claim.Errors, progress: claim.Progress}
	for _, message := range u.attached.feedback.Receive(claim, presentationTime(u.clock)) {
		u.attached.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: u.attached.source.LastSeq + 1, Agent: "Main", Kind: "progress", Text: message.Text, Observed: presentationTime(u.clock)}}})
	}
	if len(claim.Errors.Messages) > 0 {
		u.shell.Main.NoticeDetails, u.shell.Main.NoticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
	}
	u.dirty = true
	return delivery
}
