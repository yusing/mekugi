package presentation

import (
	"cmp"
	"slices"
	"strings"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity/waittargets"
)

// Waits owns wait snapshots and active presentation in the admitted turn scope.
// The application supplies authenticated roster facts and retained evidence;
// this owner neither reads storage nor changes host arguments.
type Waits struct{ turn scope.Key[waitTurn] }

type waitTurn struct {
	items map[string]*waitState
	order []string
}

type waitState struct {
	targets []waittargets.Target
	known   bool
	active  bool
	value   Value
}

type WaitAgent struct {
	Thread, Name string
	Responding   bool
}

type WaitObservation struct {
	Item          observation.Item
	Phase         string
	Observed      bool // A live or replay observation, rather than restored history.
	Caller        string
	Agents        []WaitAgent
	Snapshot      []waittargets.Target
	SnapshotFound bool
}

type WaitProgress struct {
	ID       string
	Value    Value
	Snapshot []waittargets.Target // Newly selected start-time targets, including an empty set.
	Retain   bool
}

// HasSnapshot lets the application avoid storage reads after admission.
func (w *Waits) HasSnapshot(store *scope.Store, id string) bool {
	if store == nil {
		return false
	}
	state := w.turn.Value(store).items[id]
	return state != nil && state.known
}

// Observe projects one admitted wait item. A recorded start can select targets;
// history without a start uses only retained or explicit facts. A nil turn scope
// permits late-result display without reviving the closed turn's active state.
// Source: apps/mekugi/internal/router/app_server_wait_targets.go:43:116 and
// app_server_progress.go:42:82@[a3aa6880].
func (w *Waits) Observe(store *scope.Store, in WaitObservation) WaitProgress {
	state := new(waitState)
	if store != nil && in.Item.ID != "" {
		turn := w.turn.Value(store)
		if turn.items == nil {
			turn.items = make(map[string]*waitState)
		}
		if previous := turn.items[in.Item.ID]; previous != nil {
			state = previous
		} else {
			turn.items[in.Item.ID] = state
			turn.order = append(turn.order, in.Item.ID)
		}
	}
	result := WaitProgress{ID: in.Item.ID}
	if in.Phase == "item/started" && state.value.Kind != "" {
		result.Value = state.value
		result.Value.WaitTargets = slices.Clone(state.value.WaitTargets)
		return result
	}
	names := make(map[string]string, len(in.Agents))
	for _, agent := range in.Agents {
		names[agent.Thread] = agent.Name
	}
	if !state.known && in.SnapshotFound {
		state.targets, state.known = slices.Clone(in.Snapshot), true
	}
	if !state.known && in.Observed && in.Phase == "item/started" {
		for _, id := range waitReceivers(in.Item) {
			state.targets = append(state.targets, waittargets.Target{Thread: id, Name: cmp.Or(names[id], id)})
		}
		if len(state.targets) == 0 && len(in.Item.AgentsStates) == 0 {
			prefix := in.Caller + "/"
			for _, agent := range in.Agents {
				if agent.Responding && strings.HasPrefix(agent.Name, prefix) {
					state.targets = append(state.targets, waittargets.Target{Thread: agent.Thread, Name: agent.Name})
				}
			}
			slices.SortFunc(state.targets, func(a, b waittargets.Target) int { return strings.Compare(a.Name, b.Name) })
		}
		result.Snapshot, result.Retain = slices.Clone(state.targets), true
		state.known = true
	}
	// An unfinished restored item freezes even an unavailable snapshot. It must
	// not start inferring targets from later live roster observations.
	if in.Item.Status == "inProgress" {
		state.known = true
	}
	value := Value{Kind: "progress", Body: "Waiting for agent", Live: in.Observed}
	if in.Phase == "item/completed" || in.Phase == "turn/completed" {
		value.Body = "Finished waiting"
		if in.Item.Status == "inProgress" {
			value.Body = "Wait ended"
		}
		if in.Item.Status == "failed" {
			value.Body = "Wait failed"
		}
	}
	var ids []string
	for _, target := range state.targets {
		ids = append(ids, target.Thread)
		names[target.Thread] = cmp.Or(names[target.Thread], target.Name)
	}
	for _, id := range waitReceivers(in.Item) {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		status := ""
		if in.Phase == "item/completed" {
			status = in.Item.AgentsStates[id].Status
			if status == "running" {
				status = "Still running"
			}
		}
		value.WaitTargets = append(value.WaitTargets, WaitTarget{Name: cmp.Or(names[id], id), Status: status})
	}
	state.active = in.Phase == "item/started"
	state.value = value
	result.Value = value
	result.Value.WaitTargets = slices.Clone(value.WaitTargets)
	return result
}

// EndTurn retires active presentation without claiming tool or child completion.
// Observation order, rather than map iteration or renderer retention, determines
// the order of updates. A later host result can replace these display outcomes.
// Source: apps/mekugi/internal/router/app_server_turn_lifecycle.go:48:71@[a3aa6880].
func (w *Waits) EndTurn(store *scope.Store) []WaitProgress {
	if store == nil {
		return nil
	}
	turn := w.turn.Value(store)
	var ended []WaitProgress
	for _, id := range turn.order {
		state := turn.items[id]
		if state.active {
			value := state.value
			value.Body = "Wait ended"
			value.WaitTargets = slices.Clone(value.WaitTargets)
			ended = append(ended, WaitProgress{ID: id, Value: value})
		}
	}
	*turn = waitTurn{}
	return ended
}

func waitReceivers(item observation.Item) []string {
	ids := slices.Clone(item.ReceiverThreadIDs)
	var extra []string
	for id := range item.AgentsStates {
		if !slices.Contains(ids, id) {
			extra = append(extra, id)
		}
	}
	slices.Sort(extra)
	return append(ids, extra...)
}
