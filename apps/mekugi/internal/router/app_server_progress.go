package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
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

func (u *appServerUI) readWaitEvidence(item observation.Item, key [3]string) sessionpresentation.WaitEvidence {
	s := &u.attached.session
	var evidence sessionpresentation.WaitEvidence
	if item.Type != "collabAgentToolCall" || item.Tool != "wait" || key[0] == "" || key[1] == "" || key[2] == "" || u.retainedSession.Store == nil || u.attached.waits.HasSnapshot(s.Transcript(key[0], key[1]).Turn, key[2]) {
		return evidence
	}
	var err error
	evidence.Targets, evidence.Found, err = readWaitTargets(u.retainedSession, u.attached.cwd, key)
	if err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Wait targets could not be restored: "+err.Error(), true)
	}
	return evidence
}

func (u *appServerUI) retainWaitProgress(key [3]string, progress *activitypresentation.WaitProgress) {
	if progress != nil && progress.Retain {
		if err := writeWaitTargets(u.retainedSession, u.attached.cwd, key, progress.Snapshot); err != nil {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Wait targets could not be retained: "+err.Error(), true)
		}
	}
}

// itemProgress retains the live/history storage boundary around shared projection.
func (u *appServerUI) itemProgress(item observation.Item, thread, turn, id, phase string, observed bool) (string, *activitypresentation.Value, bool) {
	key := [3]string{thread, turn, id}
	text, wait, handled, progress := u.attached.session.ProjectProgress(&u.attached.waits, item, thread, turn, id, phase, observed, u.readWaitEvidence(item, key))
	u.retainWaitProgress(key, progress)
	return text, wait, handled
}
