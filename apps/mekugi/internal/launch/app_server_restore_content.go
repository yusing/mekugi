package launch

import (
	"context"
	"iter"

	"github.com/yusing/mekugi/modules/conversation/completion"

	"github.com/yusing/mekugi/modules/conversation/composer"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"

	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

type restoredHistoryNotice struct {
	message    string
	incomplete bool // Permanent discovery/evidence gap, not a retryable item read.
}

type appServerActivityRestore struct {
	rendered bool
	notices  []restoredHistoryNotice
}

func restorePaneContent(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, restoring **appServerActivityRestore, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, reviewOwner **sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, retained changes.Scope, retainedSession *history.Session, activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, reviewSource *changecapture.Service, preparePane func(*scope.Store), root binding.ThreadInfo, dispatch func(appserver.Message) error, resumePending *[]appserver.Message) error {
	r := new(appServerActivityRestore)
	*restoring = r
	restoreDiffThread(attached, terminal, *reviewOwner, reviewSource, preparePane, root)
	attached.presentation.Status.SetText("Restoring Activity…")
	retainedRollout := readRestoredRollout(root, "/root")
	retainedRollout.events = append(retainedRollout.events, restoredHookEvents(attached, activityService, *retainedSession, r, root)...)
	var placements []*restoredPlacement
	for _, event := range retainedRollout.events {
		entry, ok := sessionpresentation.MainActivityEntry(event.entry)
		if !ok {
			continue
		}
		entry.Observed = event.at
		placements = append(placements, &restoredPlacement{entry: entry, turn: event.turn, anchor: event.anchor, at: event.at})
	}
	restoreMainHistory(attached, terminal, control, picker, editor, questions, approvals, clock, panel, resumePicker, parent, *reviewOwner, receipts, notifications, retained, *retainedSession, activityService, segments, restoredSegments, root.Turns, placements, retainedRollout.itemAt)
	r.rendered = true
	for _, notice := range r.notices {
		restoreHistoryNotice(attached, r, notice.message, notice.incomplete)
	}
	r.notices = nil
	return finishRestoredContent(attached, control, picker, restoring, reviewOwner, dispatch, resumePending)
}

func restoreDiffThread(attached *attachedPresentation, terminal *terminalsession.Attachment, review *sessionreview.Review, source *changecapture.Service, prepare func(*scope.Store), info binding.ThreadInfo) {
	if review == nil {
		return
	}
	review.IncludeThread(info.Cwd, info.ID, true)
	review.Projection.Workspace = info.Cwd
	publishReview(attached, terminal, review, source, prepare)
}

func restoreHistoryNotice(attached *attachedPresentation, restoring *appServerActivityRestore, message string, incomplete bool) {
	if incomplete {
		attached.childSource.Coverage = "History incomplete"
	}
	if r := restoring; r != nil && !r.rendered {
		r.notices = append(r.notices, restoredHistoryNotice{message, incomplete}) // Shown after Main's history.
		return
	}
	attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: attached.source.LastSeq + 1, Agent: "Session", Kind: "text", Text: message, Observed: time.Now()}}})
	// Notices advance Main independently of the Activity sequence.
	attached.session.Activity().AdvanceSequence(attached.source.LastSeq)
}

// Scope publication and durable hydration are separate events. Keep Enter
// guarded until the controller has consumed the final scope, not just until
// Codex's history replies have arrived.
func finishRestoredContent(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, restoring **appServerActivityRestore, reviewOwner **sessionreview.Review, dispatch func(appserver.Message) error, resumePending *[]appserver.Message) error {
	r := (*restoring)
	if r == nil || !r.rendered {
		return nil
	}
	if (*reviewOwner) != nil && (*reviewOwner).Restoring() {
		attached.presentation.Status.SetText("Restoring saved Diff…")
		return nil
	}

	(*restoring) = nil
	attached.presentation.Status.SetText("Ready")
	if control.Turn != "" {
		attached.presentation.Status.SetText("Working")
	}
	if err := replayResumePending(dispatch, resumePending); err != nil {
		return err
	}
	if original := control.Startup.RewindOriginal; original != "" {
		control.Startup.RewindOriginal = ""
		if attached.rewindArchive != nil {
			attached.rewindArchive(original)
		} else {
			showRewindArchive(picker, original)
		}
	}
	return nil
}

func showRewindArchive(picker *composerPicker, original string) {
	picker.openMenu("archive")
	picker.archiveTarget = original
	picker.Target, picker.Problem, picker.Loading = completion.Target{}, "", false
	picker.Choices = []completion.Choice{
		{ID: 1, Name: "No, keep the original", Description: "Continue in the new branch"},
		{ID: archiveConfirmChoice, Name: "Yes, archive the original", Description: "Keep the new branch and all workspace files"},
	}
}

func historyTime(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func restoredAgentIntervals(turns []binding.HistoryTurn) iter.Seq2[time.Time, time.Time] {
	return func(yield func(time.Time, time.Time) bool) {
		for _, turn := range turns {
			if !yield(historyTime(turn.StartedAt), historyTime(turn.CompletedAt)) {
				return
			}
		}
	}
}
