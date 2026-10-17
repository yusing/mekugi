package router

import (
	"github.com/yusing/mekugi/contracts/observation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/activity/waittargets"
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

// waitEvidence is admitted storage input, separate from wait projection.
type waitEvidence struct {
	targets []waittargets.Target
	found   bool
}

func (u *appServerUI) readWaitEvidence(item observation.Item, key [3]string) waitEvidence {
	s := &u.session
	var evidence waitEvidence
	if item.Type != "collabAgentToolCall" || item.Tool != "wait" || key[0] == "" || key[1] == "" || key[2] == "" || u.retainedSession.Store == nil || u.waits.HasSnapshot(s.Transcript(key[0], key[1]).Turn, key[2]) {
		return evidence
	}
	var err error
	evidence.targets, evidence.found, err = u.readWaitTargets(key)
	if err != nil {
		u.setNotice("Wait targets could not be restored: "+err.Error(), true)
	}
	return evidence
}

func (u *appServerUI) retainWaitProgress(key [3]string, progress *activitypresentation.WaitProgress) {
	if progress != nil && progress.Retain {
		if err := u.writeWaitTargets(key, progress.Snapshot); err != nil {
			u.setNotice("Wait targets could not be retained: "+err.Error(), true)
		}
	}
}

// itemProgress retains the live/history storage boundary around shared projection.
func (u *appServerUI) itemProgress(item observation.Item, thread, turn, id, phase string, observed bool) (string, *activitypresentation.Value, bool) {
	key := [3]string{thread, turn, id}
	text, wait, handled, progress := u.projectProgress(item, thread, turn, id, phase, observed, u.readWaitEvidence(item, key))
	u.retainWaitProgress(key, progress)
	return text, wait, handled
}

func (u *appServerUI) projectProgress(item observation.Item, thread, turn, id, phase string, observed bool, evidence waitEvidence) (string, *activitypresentation.Value, bool, *activitypresentation.WaitProgress) {
	s := &u.session
	if item.Type == "contextCompaction" {
		if phase == "item/completed" {
			return "Context compacted", nil, true, nil
		}
		return "", nil, true, nil
	}
	if item.Type != "collabAgentToolCall" || item.Tool != "wait" {
		return "", nil, false, nil
	}
	in := activitypresentation.WaitObservation{
		Item:  item,
		Phase: phase, Observed: observed && thread != "" && turn != "" && id != "", Caller: s.Caller(thread),
		Snapshot: evidence.targets, SnapshotFound: evidence.found,
	}
	in.Item.ID = id
	for _, agent := range s.Roster() {
		in.Agents = append(in.Agents, activitypresentation.WaitAgent{Thread: agent.Thread, Name: agent.Name, Responding: agent.Responding})
	}
	progress := u.waits.Observe(s.Transcript(thread, turn).Turn, in)
	return (activityui.Block{Value: progress.Value}).ProgressText(), &progress.Value, true, &progress
}

// Main progress consumes Activity's authoritative retained item evidence. The
// application adapts host facts; session presentation owns precedence and keys.
func (u *appServerUI) observeProgress(p observation.Event) {
	method := string(p.Kind)
	m := u.sessionPresentation()
	if method == "item/commandExecution/terminalInteraction" {
		m.TerminalInteraction(p.ThreadID, p.TurnID, u.Turn, p.ProcessID, p.Stdin)
		return
	}
	completed := false
	if p.ThreadID == u.Thread && method == "item/started" && p.Item.Type == "contextCompaction" {
		for _, entry := range u.source.Model.Entries {
			native := entry.Source.Native
			if native != nil && native.Thread == p.ThreadID && native.Turn == p.TurnID && native.Item == p.Item.ID && native.Phase == "item/completed" {
				completed = true
				break
			}
		}
	}
	m.ObserveProgress(p, u.Turn, completed)
}
