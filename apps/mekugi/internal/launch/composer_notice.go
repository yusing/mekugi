package launch

import (
	"cmp"
	"crypto/rand"
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/notifications/notice"
)

// Successful feedback is transient; actionable errors remain until editing.
func setNotice(feedback *noticepresentation.Model, at time.Time, text string, alert bool) {
	feedback.Set(text, alert, at)
}

func expireNotice(feedback *noticepresentation.Model, now time.Time) bool {
	return feedback.Expire(now)
}

// Claims retain only the identity they published and their independent queues.
// Entries are the Main transcript rows that show the progress batch.
type nativeCriticalNoticeDelivery struct {
	id                   string
	attachment, feedback uint64
	entries              []uint64
	errors, progress     notice.Batch
}

// Recreate pending rows aged out of the bounded transcript, keeping every
// surviving row shared by all views.
func (d *nativeCriticalNoticeDelivery) refreshProgress(attached *attachedPresentation, now time.Time) {
	if d == nil || len(d.progress.Messages) == 0 {
		return
	}
	d.entries = attached.noticeProgress.Rows(d.progress, func(seq uint64) bool {
		_, present := slices.BinarySearchFunc(attached.source.Entries, seq, func(row sessionpresentation.ActivityRecord, seq uint64) int {
			return cmp.Compare(row.Source.Seq, seq)
		})
		return present
	}, func(message notice.Message) uint64 {
		seq := attached.source.LastSeq + 1
		attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: seq, Agent: "Main", Kind: "progress", Text: message.Text, Observed: now}}})
		return seq
	})
}

func (d *nativeCriticalNoticeDelivery) finish(receipt terminalsession.NoticeReceipt) {
	if d == nil {
		return
	}
	attached := receipt.Attachment == d.attachment
	d.errors.Finish(attached && receipt.Feedback == d.feedback && receipt.Composer)
	d.progress.Finish(attached && receipt.Content)
}

func applyCriticalNotices(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring **appServerActivityRestore, side **sessioncontrol.Side, audience *sessioncontrol.Audience, issues *requestexecution.Failures) (delivery *nativeCriticalNoticeDelivery) {
	if issues != nil {
		thread := control.Thread
		if attached.presentation.DisplayingSide() && *side != nil {
			thread = (*side).Thread
		}
		attached.feedback.ClearSucceeded(issues.Notices, thread)
	}
	defer func() {
		view := publishPresentation(attached, control, picker, editor, settings, tiers, title, backend, (*restoring) != nil, (*side))
		if delivery != nil {
			delivery.attachment, delivery.feedback = view.Version.Attachment, attached.feedback.Revision
		}
	}()
	claim := claimNativeNotices(issues, control.Thread, audience)
	if claim == nil {
		return nil
	}
	delivery = &nativeCriticalNoticeDelivery{id: rand.Text(), errors: claim.Errors, progress: claim.Progress}
	attached.feedback.Receive(claim, presentationTime(clock))
	delivery.refreshProgress(attached, presentationTime(clock))
	return delivery
}
