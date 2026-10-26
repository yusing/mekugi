package sessionpresentation

import (
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type compactionMetrics struct {
	completed map[string]struct{}
	known     bool
}

var compactionMetricKey scope.Key[compactionMetrics]

// RestoreCompactions admits the complete native history baseline. Live observations
// may already be present; item identity makes restoration and repeated completions
// idempotent without keeping another transcript or durable counter.
func (s *Attachment) RestoreCompactions(thread string, completed []string) {
	store := s.Thread(thread)
	if store == nil {
		return
	}
	state := compactionMetricKey.Value(store)
	state.known = true
	for _, id := range completed {
		s.ObserveCompaction(thread, id)
	}
}

func (s *Attachment) ObserveCompaction(thread, id string) {
	store := s.Thread(thread)
	if store == nil || id == "" {
		return
	}
	state := compactionMetricKey.Value(store)
	if state.completed == nil {
		state.completed = make(map[string]struct{})
	}
	state.completed[id] = struct{}{}
}

func (s *Attachment) InspectionMetrics(thread string) control.SessionMetrics {
	var metrics control.SessionMetrics
	if s.retired {
		return metrics
	}
	if store := s.FindThread(thread); store != nil {
		state := compactionMetricKey.Value(store)
		if state.known {
			metrics.Compactions = new(uint64(len(state.completed)))
		}
	}
	if name := s.KnownCaller(thread); name != "" {
		agent := s.Agent(name)
		if agent.ContextKnown {
			metrics.ContextTokens = new(agent.ContextTokens)
		}
		if agent.ContextWindow > 0 {
			metrics.ContextWindow = new(agent.ContextWindow)
		}
	}
	return metrics
}
