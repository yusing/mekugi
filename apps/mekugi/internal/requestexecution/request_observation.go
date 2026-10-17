// Source: apps/mekugi/internal/router/request_observation.go:1:41@b9679c93.
package requestexecution

import "github.com/yusing/mekugi/contracts/inference"

type requestOutcome uint8

const (
	requestOutcomeUnknown requestOutcome = iota
	requestOutcomeCompleted
	requestOutcomeFailed
	requestOutcomeCanceledBeforeResponse
	requestOutcomeCanceledAfterResponse
	requestOutcomeTimedOut
	requestOutcomeStreamIdleTimedOut
)

func (outcome requestOutcome) String() string {
	switch outcome {
	case requestOutcomeCompleted:
		return "completed"
	case requestOutcomeFailed:
		return "failed"
	case requestOutcomeCanceledBeforeResponse:
		return "canceled_before_response"
	case requestOutcomeCanceledAfterResponse:
		return "canceled_after_response"
	case requestOutcomeTimedOut:
		return "timed_out"
	case requestOutcomeStreamIdleTimedOut:
		return "stream_idle_timed_out"
	default:
		return "unknown"
	}
}

type requestObservation struct {
	outcome requestOutcome

	usageCounts   inference.Usage
	usageObserved bool
}
