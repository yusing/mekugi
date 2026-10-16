package router

import "github.com/yusing/mekugi/backends/codex/rollout"

func (observations *sessionAXInput) observe(event rollout.InspectionEvent) error {
	switch event.Kind {
	case "item_started", "item_completed":
		if event.Kind == "item_completed" && event.PersistedTimes {
			return observations.Commands.ObserveCompleted(event.ItemID, event.CallID, event.Started, event.Completed, event.ExitCode)
		}
		return observations.Commands.Observe(event.Kind, event.ItemID, event.CallID, event.At, event.ExitCode)
	case "task_started", "task_complete", "turn_started", "turn_complete":
		observations.Completion.Observe(event.Kind, event.TurnID, event.At)
	}
	return nil
}
