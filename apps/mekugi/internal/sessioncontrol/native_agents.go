package sessioncontrol

import (
	"context"
	json "encoding/json/v2"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/contracts/observation"
)

// NativeWaitLabel projects the native wait reason without creating a deadline.
func NativeWaitLabel(wait appserver.AgentWaitObservation) string {
	retry := ""
	if wait.DeadlineUnixMS != nil {
		retry = " · retry at " + time.UnixMilli(*wait.DeadlineUnixMS).Local().Format("15:04:05 MST Jan 2")
	}
	reason := ""
	if wait.Reason != nil {
		reason = *wait.Reason
	}
	switch reason {
	case "providerCapacity":
		return "Waiting for provider capacity" + retry
	case "providerUsageLimit":
		if wait.DeadlineUnixMS == nil {
			return "Usage limit · paused"
		}
		return "Waiting for usage limit reset" + retry
	default:
		return "Waiting for agents"
	}
}

func NativeAgentDetails(agent appserver.AgentObservation) observation.SessionDetails {
	details := observation.SessionDetails{Model: agent.Model, ReasoningEffort: agent.ReasoningEffort, ServiceTier: agent.ServiceTier}
	if value := agent.Metrics.ContextTokens; value != nil && *value >= 0 {
		details.ContextTokens = new(uint64(*value))
	}
	if value := agent.Metrics.ContextWindow; value != nil && *value >= 0 {
		details.ContextWindow = new(uint64(*value))
	}
	details.Compactions = agent.Metrics.CompletedCompactions
	return details
}

// ObserveAgent uses native passive observation even for retained, unloaded
// children. It never resumes a runtime or recounts its rollout.
func (r *Requests[T]) ObserveAgent(ctx context.Context, thread string) (observation.SessionDetails, error) {
	raw, err := r.call(ctx, thread, "agent/observe", map[string]any{"threadId": thread})
	if err != nil {
		return observation.SessionDetails{}, err
	}
	var result struct {
		Agent appserver.AgentObservation `json:"agent"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return observation.SessionDetails{}, err
	}
	return NativeAgentDetails(result.Agent), nil
}
