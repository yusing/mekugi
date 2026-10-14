package router

import (
	"fmt"
	"slices"

	"github.com/yusing/mekugi/internal/orchestrate"
)

// Project retained facts only. Full records remain available without changing
// the storage model or treating inspection as host or filesystem reconciliation.
func orchestrateListAgents(batches []orchestrate.Batch, input orchestrateListInput) ([]any, error) {
	targets, err := orchestrateWaitTargets(input.Targets)
	if err != nil {
		return nil, err
	}
	switch input.Detail {
	case "", "summary", "checks", "reviews", "full":
	default:
		return nil, fmt.Errorf("unknown list detail %q", input.Detail)
	}
	for _, target := range targets {
		if !slices.ContainsFunc(batches, func(b orchestrate.Batch) bool { return b.TaskName == target }) {
			return nil, fmt.Errorf("list target %q is not a retained batch in this coordinator run", target)
		}
	}
	agents := make([]any, 0, len(batches))
	for _, b := range batches {
		if len(targets) != 0 && !slices.Contains(targets, b.TaskName) {
			continue
		}
		if input.Detail == "full" {
			agents = append(agents, b)
			continue
		}
		agent := map[string]any{"task_name": b.TaskName, "state": b.State}
		if b.Error != "" {
			agent["error"] = b.Error
		}
		if l := b.Launch; l != nil {
			// Empty identities preserve a launch intent without confirmed effects.
			agent["launch"] = map[string]any{"thread_id": l.ThreadID, "turn_id": l.TurnID, "host_status": l.HostStatus}
		}
		if w := b.Workflow; w != nil {
			workflow := map[string]any{"kind": w.Assignment.Kind, "phase": w.Phase, "generation": w.Generation}
			if w.ReviewWaiver != "" {
				workflow["review_waiver"] = w.ReviewWaiver
			}
			if w.Candidate != nil {
				workflow["candidate"] = w.Candidate
			}
			if w.Reason != "" {
				workflow["reason"] = w.Reason
			}
			if w.Outcome != nil {
				workflow["outcome_state"] = w.Outcome.State
			}
			var dispatches []orchestrate.WorkflowDispatch
			for _, d := range w.Dispatches {
				if d.Generation == w.Generation || workflowDispatchUnsettled(d) {
					d.Params = nil // Launch configuration belongs to explicit full inspection.
					dispatches = append(dispatches, d)
				}
			}
			if len(dispatches) != 0 {
				workflow["dispatches"] = dispatches
			}
			switch input.Detail {
			case "checks":
				workflow["required_checks"], workflow["checks"] = w.Assignment.RequiredChecks, w.Checks
			case "reviews":
				workflow["reviews"] = w.Reviews
				if len(w.PreviousReviews) != 0 {
					workflow["previous_reviews"] = w.PreviousReviews
				}
			}
			agent["workflow"] = workflow
		}
		var evidence []map[string]any
		for _, e := range b.Evidence {
			if e.State != "ready" && e.State != "removed" || e.Error != "" {
				evidence = append(evidence, map[string]any{"name": e.Name, "state": e.State, "error": e.Error})
			}
		}
		if len(evidence) != 0 {
			agent["evidence"] = evidence
		}
		if b.ShadowMerge != nil {
			agent["shadow_merge"] = map[string]any{"state": b.ShadowMerge.State}
		}
		agents = append(agents, agent)
	}
	return agents, nil
}
