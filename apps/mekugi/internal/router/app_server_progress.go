package router

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/activity/waittargets"
)

// History can retain an unfinished item in a stopped turn. The owning turn
// ends its waiting presentation without claiming a successful tool result.
func appServerHistoryProgressPhase(item binding.ThreadItem, turnStatus string) string {
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

func (u *appServerUI) readWaitEvidence(item binding.ThreadItem, key [3]string) waitEvidence {
	s := &u.session
	var evidence waitEvidence
	if item.Type != "collabAgentToolCall" || item.Tool != "wait" || key[0] == "" || key[1] == "" || key[2] == "" || s.waitStore == nil || s.waits.HasSnapshot(s.Transcript(key[0], key[1]).Turn, key[2]) {
		return evidence
	}
	var err error
	evidence.targets, evidence.found, err = s.readWaitTargets(key)
	if err != nil {
		u.setNotice("Wait targets could not be restored: "+err.Error(), true)
	}
	return evidence
}

func (u *appServerUI) retainWaitProgress(key [3]string, progress *activitypresentation.WaitProgress) {
	if progress != nil && progress.Retain {
		if err := u.session.writeWaitTargets(key, progress.Snapshot); err != nil {
			u.setNotice("Wait targets could not be retained: "+err.Error(), true)
		}
	}
}

// itemProgress retains the live/history storage boundary around shared projection.
func (u *appServerUI) itemProgress(item binding.ThreadItem, thread, turn, id, phase string, observed bool) (string, *activitypresentation.Value, bool) {
	key := [3]string{thread, turn, id}
	text, wait, handled, progress := u.session.itemProgress(item, thread, turn, id, phase, observed, u.readWaitEvidence(item, key))
	u.retainWaitProgress(key, progress)
	return text, wait, handled
}

func (s *appServerSession) itemProgress(item binding.ThreadItem, thread, turn, id, phase string, observed bool, evidence waitEvidence) (string, *activitypresentation.Value, bool, *activitypresentation.WaitProgress) {
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
		Item:  observation.Item{ID: id, Status: item.Status, ReceiverThreadIDs: item.ReceiverThreadIDs},
		Phase: phase, Observed: observed && thread != "" && turn != "" && id != "", Caller: s.path(thread),
		Snapshot: evidence.targets, SnapshotFound: evidence.found,
	}
	in.Item.AgentsStates = make(map[string]observation.AgentState, len(item.AgentsStates))
	for target, state := range item.AgentsStates {
		in.Item.AgentsStates[target] = observation.AgentState{Status: state.Status}
	}
	for target, name := range s.paths {
		agent := s.agent(name)
		in.Agents = append(in.Agents, activitypresentation.WaitAgent{Thread: target, Name: name, Responding: agent != nil && agent.Responding})
	}
	progress := s.waits.Observe(s.Transcript(thread, turn).Turn, in)
	return (activityui.Block{Value: progress.Value}).ProgressText(), &progress.Value, true, &progress
}

// Main progress consumes Activity's authoritative retained item evidence. The
// application adapts host facts; session presentation owns precedence and keys.
func (u *appServerUI) observeProgress(method string, p appServerEvent) {
	m := u.sessionPresentation()
	if method == "item/commandExecution/terminalInteraction" {
		m.TerminalInteraction(p.ThreadID, p.TurnID, u.Turn, p.ProcessID, p.Stdin)
		return
	}
	completed := false
	if p.ThreadID == u.Thread && method == "item/started" && p.Item.Type == "contextCompaction" {
		for _, entry := range u.view.Model.Entries {
			native := entry.Source.native
			if native != nil && native.thread == p.ThreadID && native.turn == p.TurnID && native.item == p.Item.ID && native.phase == "item/completed" {
				completed = true
				break
			}
		}
	}
	turn := p.TurnID
	if method == "turn/started" || method == "turn/completed" {
		turn = p.Turn.ID
	}
	m.ObserveProgress(observation.Event{Kind: observation.Kind(method), ThreadID: p.ThreadID, TurnID: turn,
		Item: observation.Item{ID: p.Item.ID, Type: p.Item.Type, ProcessID: p.Item.ProcessID}}, u.Turn, completed)
}
