package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/notifications/notice"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Successful feedback is transient; actionable errors remain until editing.
func (u *appServerUI) setNotice(text string, alert bool) {
	u.feedback.Set(text, alert, u.now())
	u.mainView.NoticeDetails, u.mainView.NoticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
}

func (u *appServerUI) expireNotice(now time.Time) bool {
	if !u.feedback.Expire(now) {
		return false
	}
	u.mainView.NoticeDetails, u.mainView.NoticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
	return true
}

func (u *appServerUI) composerNoticeMouse(button, x, y int, release bool) bool {
	details, dismiss := u.mainView.NoticeDetails.Contains(x, y), u.mainView.NoticeDismiss.Contains(x, y)
	if (!details && !dismiss) || terminalsession.MainStatusDetails(u.sessionPresentation().View().Status) == "" {
		return false
	}
	if !release && button == 0 {
		if dismiss {
			u.setNotice("", false)
			if u.sessionPresentation().Status.Alert {
				u.sessionPresentation().Status.Set("", false)
			}
		} else {
			u.shell.openBlocks(u.view, []activityui.Block{{Kind: "error", Body: terminalsession.MainStatusDetails(u.sessionPresentation().View().Status)}})
		}
		u.dirty = true
	}
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
		view := u.publishPresentation(u.now())
		if delivery != nil {
			delivery.attachment, delivery.feedback = view.Version.Attachment, u.feedback.Revision
		}
	}()
	claim := claimNativeNotices(u.issues, u.Thread, u.audience)
	if claim == nil {
		return nil
	}
	delivery = &nativeCriticalNoticeDelivery{errors: claim.Errors, progress: claim.Progress}
	for _, message := range u.feedback.Receive(claim, u.now()) {
		u.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: u.source.LastSeq + 1, Agent: "Main", Kind: "progress", Text: message.Text, Observed: u.now()}}})
	}
	if len(claim.Errors.Messages) > 0 {
		u.mainView.NoticeDetails, u.mainView.NoticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
	}
	u.dirty = true
	return delivery
}
