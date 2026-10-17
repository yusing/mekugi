package router

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
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

// itemProgress adapts host progress and supplies admitted wait evidence. Activity
// owns wait selection and lifecycle; storage failures remain auxiliary notices.
func (u *appServerUI) itemProgress(item binding.ThreadItem, thread, turn, id, phase string, observed bool) (string, *activitypresentation.Value, bool) {
	if item.Type == "contextCompaction" {
		if phase == "item/completed" {
			return "Context compacted", nil, true
		}
		return "", nil, true
	}
	if item.Type != "collabAgentToolCall" || item.Tool != "wait" {
		return "", nil, false
	}
	s := &u.session
	in := activitypresentation.WaitObservation{
		Item:  observation.Item{ID: id, Status: item.Status, ReceiverThreadIDs: item.ReceiverThreadIDs},
		Phase: phase, Observed: observed && thread != "" && turn != "" && id != "", Caller: s.path(thread),
	}
	in.Item.AgentsStates = make(map[string]observation.AgentState, len(item.AgentsStates))
	for target, state := range item.AgentsStates {
		in.Item.AgentsStates[target] = observation.AgentState{Status: state.Status}
	}
	for target, name := range s.paths {
		agent := s.agent(name)
		in.Agents = append(in.Agents, activitypresentation.WaitAgent{Thread: target, Name: name, Responding: agent != nil && agent.Responding})
	}
	store := s.Transcript(thread, turn).Turn
	key := [3]string{thread, turn, id}
	if thread != "" && turn != "" && id != "" && !s.waits.HasSnapshot(store, id) && s.waitStore != nil {
		var err error
		in.Snapshot, in.SnapshotFound, err = s.readWaitTargets(key)
		if err != nil {
			u.setNotice("Wait targets could not be restored: "+err.Error(), true)
		}
	}
	progress := s.waits.Observe(store, in)
	if progress.Retain {
		if err := s.writeWaitTargets(key, progress.Snapshot); err != nil {
			u.setNotice("Wait targets could not be retained: "+err.Error(), true)
		}
	}
	return (activityui.Block{Value: progress.Value}).ProgressText(), &progress.Value, true
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
