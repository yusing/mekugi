package router

import (
	"time"

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
	if (!details && !dismiss) || terminalsession.MainStatusDetails(u.mainPresentation(u.now()).Status) == "" {
		return false
	}
	if !release && button == 0 {
		if dismiss {
			u.setNotice("", false)
			if u.sessionPresentation().Status.Alert {
				u.sessionPresentation().Status.Set("", false)
			}
		} else {
			u.shell.openBlocks(u.view, []activityui.Block{{Kind: "error", Body: terminalsession.MainStatusDetails(u.mainPresentation(u.now()).Status)}})
		}
		u.dirty = true
	}
	return true
}

// Native delivery keeps the batch's complete composer text beside its claim.
// This lets terminal acknowledgement distinguish visible errors from overlays.
type nativeCriticalNoticeDelivery struct {
	ui               *appServerUI
	composerText     string
	errors, progress notice.Batch
}

// A successful write can show the composer while transcript progress is hidden.
// Each subset keeps the queue owner's original claim and repeat-count snapshot.
func (d *nativeCriticalNoticeDelivery) finish(success bool) {
	if d == nil {
		return
	}
	u := d.ui
	visible := success && (u.shell == nil || u.shell.output == nil && u.shell.selection == nil)
	d.errors.Finish(visible && u.feedback.Text == d.composerText && u.mainView.NoticeDismiss.W > 0)
	d.progress.Finish(visible && u.mainView.ContentPainted)
}

func (u *appServerUI) applyCriticalNotices() *nativeCriticalNoticeDelivery {
	var activity *subagentActivity
	if u.proxy != nil {
		activity = u.proxy.activity
	}
	claim := u.issues.takeNative(u.Thread, activity)
	if claim == nil {
		return nil
	}
	delivery := &nativeCriticalNoticeDelivery{ui: u, errors: claim.Errors, progress: claim.Progress}
	for _, message := range u.feedback.Receive(claim, u.now()) {
		u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.LastSeq + 1, Agent: "Main", Kind: "progress", Text: message.Text, Observed: u.now()}}})
	}
	if len(claim.Errors.Messages) > 0 {
		delivery.composerText = u.feedback.Text
		u.mainView.NoticeDetails, u.mainView.NoticeDismiss = terminalsession.Rect{}, terminalsession.Rect{}
	}
	u.dirty = true
	return delivery
}
