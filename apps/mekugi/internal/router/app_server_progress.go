package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/observation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// History can retain an unfinished item in a stopped turn. The owning turn
// ends its waiting presentation without claiming a successful tool result.
func appServerHistoryProgressPhase(item observation.Item, turnStatus string) string {
	if item.Status == "inProgress" {
		if item.Type == "collabAgentToolCall" && item.Tool == "wait" && (turnStatus == "completed" || turnStatus == "interrupted" || turnStatus == "failed") {
			return "turn/completed"
		}
		return "item/started"
	}
	return "item/completed"
}

func readWaitEvidence(attached *attachedPresentation, terminal *terminalsession.Attachment, retainedSession history.Session, clock func() time.Time, item observation.Item, key [3]string) sessionpresentation.WaitEvidence {
	s := &attached.session
	var evidence sessionpresentation.WaitEvidence
	if item.Type != "collabAgentToolCall" || item.Tool != "wait" || key[0] == "" || key[1] == "" || key[2] == "" || retainedSession.Store == nil || attached.waits.HasSnapshot(s.Transcript(key[0], key[1]).Turn, key[2]) {
		return evidence
	}
	var err error
	evidence.Targets, evidence.Found, err = readWaitTargets(retainedSession, attached.cwd, key)
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait targets could not be restored: "+err.Error(), true)
	}
	return evidence
}

func retainWaitProgress(attached *attachedPresentation, terminal *terminalsession.Attachment, retainedSession history.Session, clock func() time.Time, key [3]string, progress *activitypresentation.WaitProgress) {
	if progress != nil && progress.Retain {
		if err := writeWaitTargets(retainedSession, attached.cwd, key, progress.Snapshot); err != nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait targets could not be retained: "+err.Error(), true)
		}
	}
}

// itemProgress retains the live/history storage boundary around shared projection.
func itemProgress(attached *attachedPresentation, terminal *terminalsession.Attachment, retainedSession history.Session, clock func() time.Time, item observation.Item, thread, turn, id, phase string, observed bool) (string, *activitypresentation.Value, bool) {
	key := [3]string{thread, turn, id}
	text, wait, handled, progress := attached.session.ProjectProgress(&attached.waits, item, thread, turn, id, phase, observed, readWaitEvidence(attached, terminal, retainedSession, clock, item, key))
	retainWaitProgress(attached, terminal, retainedSession, clock, key, progress)
	return text, wait, handled
}
