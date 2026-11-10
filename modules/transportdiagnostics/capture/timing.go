package capture

import (
	"context"
	"slices"
	"time"
)

const maxTimingAttempts = 8

// TimingPhase carries a wall-clock endpoint and a monotonic offset from admission.
// A nil phase is unknown, not an elapsed time of zero.
type TimingPhase struct {
	At           time.Time `json:"at"`
	OffsetMillis float64   `json:"offset_ms"`
}

type AttemptTiming struct {
	Number           uint64       `json:"number"`
	Transport        string       `json:"transport"`
	Started          TimingPhase  `json:"started"`
	Sent             *TimingPhase `json:"request_sent"`
	SendObservations uint64       `json:"send_observations"`
	FirstOutput      *TimingPhase `json:"first_output"`
	Completed        *TimingPhase `json:"completed"`
	Status           int          `json:"status"`
	Outcome          string       `json:"outcome"`
}

type RequestTiming struct {
	CaptureID       string          `json:"capture_id"`
	Sequence        uint64          `json:"request_sequence"`
	Started         time.Time       `json:"started_at"`
	ToolResultReady *TimingPhase    `json:"tool_result_ready"`
	ForwardStarted  *TimingPhase    `json:"forward_started"`
	Completed       *TimingPhase    `json:"completed"`
	Outcome         string          `json:"outcome"`
	Attempts        []AttemptTiming `json:"attempts"`
	OmittedAttempts uint64          `json:"omitted_attempts"`
}

func (s *requestState) phase(at time.Time) *TimingPhase {
	return &TimingPhase{At: at.UTC(), OffsetMillis: float64(at.Sub(s.timing.Started)) / float64(time.Millisecond)}
}

// ObserveForwardStart ends local request preparation, before provider routing.
func ObserveForwardStart(ctx context.Context) {
	if s, ok := ctx.Value(captureKey{}).(*requestState); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.timing.ForwardStarted = s.phase(time.Now())
	}
}

// ObserveRequestOutcome accepts only execution's bounded terminal categories.
func ObserveRequestOutcome(ctx context.Context, outcome string) {
	if s, ok := ctx.Value(captureKey{}).(*requestState); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		if validTimingOutcome(outcome) {
			s.timing.Outcome = outcome
		} else {
			s.timing.Outcome = "unknown"
		}
	}
}

func validTimingOutcome(outcome string) bool {
	switch outcome {
	case "unknown", "completed", "failed", "canceled_before_response", "canceled_after_response", "timed_out", "stream_idle_timed_out":
		return true
	default:
		return false
	}
}

func (s *requestState) startTimingAttempt(number uint64, transport string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.timing.Attempts) == maxTimingAttempts {
		s.timing.OmittedAttempts++
		return
	}
	s.timing.Attempts = append(s.timing.Attempts, AttemptTiming{Number: number, Transport: transport, Started: *s.phase(at), Outcome: "unknown"})
}

func (s *requestState) timingAttempt(number uint64, update func(*AttemptTiming)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.timing.Attempts {
		if s.timing.Attempts[i].Number == number {
			update(&s.timing.Attempts[i])
			return
		}
	}
}

func (s *requestState) sent(number uint64) {
	s.timingAttempt(number, func(a *AttemptTiming) {
		a.SendObservations++
		// net/http can retry inside one RoundTrip. Do not pair its final
		// response with an arbitrarily selected wire send.
		if a.SendObservations == 1 {
			a.Sent = s.phase(time.Now())
		} else {
			a.Sent = nil
		}
	})
}

func (s *requestState) firstOutput(number uint64) {
	s.timingAttempt(number, func(a *AttemptTiming) {
		if a.FirstOutput == nil {
			a.FirstOutput = s.phase(time.Now())
		}
	})
}

func (s *requestState) completeTimingAttempt(number uint64, at time.Time, status int, failed bool) {
	s.timingAttempt(number, func(a *AttemptTiming) {
		a.Completed, a.Status, a.Outcome = s.phase(at), status, "completed"
		if failed || status >= 400 {
			a.Outcome = "failed"
		}
	})
}

func (s *requestState) completedTiming(at time.Time) RequestTiming {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.timing
	result.Started = result.Started.UTC()
	result.Completed = s.phase(at)
	result.Attempts = slices.Clone(result.Attempts)
	return result
}

// Sent marks a successfully written response.create, never a handshake or an
// automatic successor for which no new request was sent.
func (attempt *WebSocketAttempt) Sent() {
	if attempt != nil {
		attempt.state.sent(attempt.attempt)
	}
}
