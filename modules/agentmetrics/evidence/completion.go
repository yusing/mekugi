// Source: capturer/ax.go@fdb889f8.
package evidence

import (
	"time"
)

type AXCompletionMetrics struct {
	State          string `json:"state"`
	CompletedTurns uint64 `json:"completed_turns"`
	DurationMS     int64  `json:"duration_ms"`
	UnpairedEvents uint64 `json:"unpaired_events"`
}

// AXCompletionAccumulator pairs actual task/turn events by identity. It does not
// infer completion from silence, a tool exit, or an assistant's prose.
type AXCompletionAccumulator struct {
	Metrics   AXCompletionMetrics
	starts    map[string]axCompletionStart
	completed map[string]bool
}

type axCompletionStart struct {
	kind string
	at   time.Time
}

func (accumulator *AXCompletionAccumulator) Observe(kind, id string, at time.Time) {
	if accumulator.starts == nil {
		accumulator.completed = make(map[string]bool)
		accumulator.starts = make(map[string]axCompletionStart)
	}
	if id == "" || at.IsZero() {
		accumulator.Metrics.UnpairedEvents++
		return
	}
	if accumulator.completed[id] {
		accumulator.Metrics.UnpairedEvents++
		return
	}
	if kind == "task_started" || kind == "turn_started" {
		if _, exists := accumulator.starts[id]; exists {
			accumulator.Metrics.UnpairedEvents++
			return
		}
		accumulator.starts[id] = axCompletionStart{kind: kind, at: at}
		return
	}
	start, exists := accumulator.starts[id]
	matchingKind := start.kind == "task_started" && kind == "task_complete" ||
		start.kind == "turn_started" && kind == "turn_complete"
	if !exists || !matchingKind || at.Before(start.at) {
		accumulator.Metrics.UnpairedEvents++
		return
	}
	delete(accumulator.starts, id)
	accumulator.completed[id] = true
	accumulator.Metrics.CompletedTurns++
	accumulator.Metrics.DurationMS = saturatedAXDuration(accumulator.Metrics.DurationMS, at.Sub(start.at).Milliseconds())
}

func (accumulator *AXCompletionAccumulator) Result() AXCompletionMetrics {
	result := accumulator.Metrics
	result.UnpairedEvents += uint64(len(accumulator.starts))
	result.State = "unavailable"
	if result.CompletedTurns > 0 || result.UnpairedEvents > 0 {
		result.State = "observed"
	}
	return result
}
