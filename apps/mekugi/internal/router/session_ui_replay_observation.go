package router

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
)

// replayPresentationEvent adapts decoded read-only values to the existing
// presentation owner. It never serializes them back into a host RPC envelope.
func replayPresentationEvent(e observation.Event) appServerEvent {
	i := e.Item
	item := binding.ThreadItem{ID: i.ID, Type: i.Type, Status: i.Status,
		Phase: i.Phase, Cwd: i.Cwd, Kind: i.Kind, Path: i.Path, Command: i.Command, ProcessID: i.ProcessID,
		ClientID: i.ClientID, AgentThreadID: i.AgentThreadID, AgentPath: i.AgentPath, Tool: i.Tool,
		Prompt: i.Prompt, Model: i.Model, ReasoningEffort: i.ReasoningEffort,
		SenderThreadID: i.SenderThreadID, ReceiverThreadIDs: i.ReceiverThreadIDs, DurationMS: i.DurationMS,
		AggregatedOutput: i.AggregatedOutput, ExitCode: i.ExitCode}
	for _, change := range i.Changes {
		projected := binding.FileChange{Path: change.Path, Diff: change.Diff}
		projected.Kind.Type, projected.Kind.MovePath = change.Kind.Type, change.Kind.MovePath
		item.Changes = append(item.Changes, projected)
	}
	if i.AgentsStates != nil {
		item.AgentsStates = make(map[string]binding.AgentState, len(i.AgentsStates))
		for id, state := range i.AgentsStates {
			item.AgentsStates[id] = binding.AgentState{Status: state.Status}
		}
	}
	return appServerEvent{text: binding.RetainedTranscript(e), ThreadID: e.ThreadID, TurnID: e.TurnID, ItemID: e.ItemID, Delta: e.Delta, Item: item, Turn: appServerTurn{ID: e.TurnID, Status: e.TurnStatus}}
}
