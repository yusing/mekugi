package launch

import (
	"context"
	"iter"

	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/modules/sessionhistory/history"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

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

func (session *nativeSession) restorePaneContent(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, root binding.ThreadInfo) error {
	r := new(appServerActivityRestore)
	session.restoring = r
	restoreDiffThread(attached, session.review, session.reviewSource, root)
	attached.presentation.Status.SetText("Restoring Activity…")
	var content retainedActivity
	if session.hydration != nil {
		content = session.hydration.content
	} else {
		content = readRetainedActivity(parent, session.retainedSession, root)
	}
	root = content.thread
	retainedRollout := content.rollout
	r.notices = append(r.notices, content.notices...)
	for _, observation := range content.hooks {
		entry := hookEntry(&attached.session, session.activity, attached.cwd, root.ID, observation, false)
		retainedRollout.events = append(retainedRollout.events, restoredRolloutEvent{at: observation.At, entry: entry})
	}
	var placements []*restoredPlacement
	for _, event := range retainedRollout.events {
		entry, ok := sessionpresentation.MainActivityEntry(event.entry)
		if !ok {
			continue
		}
		entry.Observed = event.at
		placements = append(placements, &restoredPlacement{entry: entry, turn: event.turn, anchor: event.anchor, at: event.at})
	}
	session.restoreMainHistory(attached, picker, editor, clock, panel, resumePicker, parent, root.Turns, placements, retainedRollout.itemAt)
	applyCompactionObservations(attached, r, func(thread, item string) (*history.CompactionObservation, int, error) {
		return content.compactions[[2]string{thread, item}], 0, nil
	})
	r.rendered = true
	for _, notice := range r.notices {
		restoreHistoryNotice(attached, r, notice.message, notice.incomplete)
	}
	r.notices = nil
	return finishRestoredContent(attached, &session.Client, picker, &session.restoring, &session.review, session.dispatch, &session.resumePending)
}

func restoreDiffThread(attached *attachedPresentation, review *sessionreview.Review, source *changecapture.Service, info binding.ThreadInfo) {
	if review == nil {
		return
	}
	review.IncludeThread(info.Cwd, info.ID, true)
	review.Projection.Workspace = info.Cwd
	publishReview(attached, review, source)
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

	return nil
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
