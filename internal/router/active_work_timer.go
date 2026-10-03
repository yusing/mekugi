package router

import "time"

// activeWorkTimer accumulates observed work intervals, never idle gaps.
// Since is a live anchor; replay keeps ElapsedNS but must not revive the anchor.
type activeWorkTimer struct {
	ElapsedNS int64     `json:"elapsed_ns,omitzero"`
	Since     time.Time `json:"since,omitzero"`
	Known     bool      `json:"known,omitzero"`
}

func (timer activeWorkTimer) at(now time.Time) time.Duration {
	if !timer.Since.IsZero() {
		return time.Duration(timer.ElapsedNS) + max(0, now.Sub(timer.Since))
	}
	return time.Duration(timer.ElapsedNS)
}

func (timer *activeWorkTimer) update(running bool, now time.Time) {
	timer.ElapsedNS = int64(timer.at(now))
	timer.Since = time.Time{}
	if running {
		timer.Known = true
		timer.Since = now
	}
}
