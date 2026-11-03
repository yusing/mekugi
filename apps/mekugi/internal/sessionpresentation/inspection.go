package sessionpresentation

import (
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type compactionMetrics struct {
	completed map[string]struct{}
	known     bool
	started   string
	progress  *activity.Value
	context   *observation.Context
	at        time.Time
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

// ObserveCompactionStart invalidates only the pre-compaction measurement. Native
// Codex can publish the fresh measurement before the completion notification.
func (s *Attachment) ObserveCompactionStart(thread, id string) {
	store := s.Thread(thread)
	if store == nil || id == "" {
		return
	}
	state := compactionMetricKey.Value(store)
	if _, completed := state.completed[id]; completed || state.started == id {
		return
	}
	state.started = id
	state.at = time.Time{}
	state.context = nil
	state.progress = &activity.Value{Kind: "progress", Body: "Compacting context", Live: true}
	if name := s.KnownCaller(thread); name != "" {
		agent := s.Agent(name)
		if agent.ContextKnown {
			state.context = &observation.Context{Tokens: agent.ContextTokens, Window: agent.ContextWindow, Known: true}
		}
		agent.ContextKnown = false
	}
}

func (s *Attachment) compactionDetails(thread, id string, now time.Time, native *NativeItem) {
	state := compactionMetricKey.Value(s.Thread(thread))
	if state.started == id {
		if state.context != nil {
			native.CompactionContext = state.context
		}
		if !native.CompactionTimed && !state.at.IsZero() {
			native.Duration = max(0, now.Sub(state.at))
			native.CompactionTimed = true
		}
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
	s.finishCompaction(thread, id)
}

func (s *Attachment) finishCompaction(thread, id string) {
	if store := s.FindThread(thread); store != nil {
		state := compactionMetricKey.Value(store)
		if state.started == id {
			state.progress = nil
		}
	}
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
