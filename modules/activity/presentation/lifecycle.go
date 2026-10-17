package presentation

import (
	"iter"
	"time"
)

// AgentLifecycle records admitted observations, not host operation authority.
// Source: apps/mekugi/internal/router/app_server_events.go:244:311@[219eb509]
type AgentLifecycle struct {
	WorkTimer    WorkTimer `json:",omitzero"`
	Responding   bool      `json:",omitzero"`
	Final        bool      `json:",omitzero"`
	Started      time.Time `json:",omitzero"`
	LastResponse time.Time `json:",omitzero"`
	Turns        uint64    `json:",omitzero"`
}

func (a *AgentLifecycle) ObserveActive(active bool, at time.Time) {
	// Status alone cannot establish the first turn's start.
	if active && a.Started.IsZero() {
		return
	}
	a.WorkTimer.update(active, at)
	a.Responding = active
	if active {
		a.Final = false
	}
}

func (a *AgentLifecycle) TurnStarted(at time.Time) {
	a.WorkTimer.update(true, at)
	a.Responding, a.Final = true, false
	if a.Started.IsZero() {
		a.Started = at
	}
	a.Turns++
}

// TurnCompleted receives only a completion admitted for the current turn.
func (a *AgentLifecycle) TurnCompleted(completed bool, at time.Time) {
	a.WorkTimer.update(false, at)
	a.Responding, a.LastResponse, a.Final = false, at, completed
}

// RestoreTiming replaces observed endpoints and closed work intervals without
// changing response state or reviving a live clock. The caller supplies decoded
// start/end instants and keeps its live-versus-historical admission policy.
// Source: apps/mekugi/internal/router/app_server_restore_content.go:297:322@[219eb509]
func (a *AgentLifecycle) RestoreTiming(intervals iter.Seq2[time.Time, time.Time]) {
	a.Started, a.LastResponse, a.WorkTimer = time.Time{}, time.Time{}, WorkTimer{}
	for start, end := range intervals {
		if !start.IsZero() && (a.Started.IsZero() || start.Before(a.Started)) {
			a.Started = start
		}
		if end.After(a.LastResponse) {
			a.LastResponse = end
		}
		if start.IsZero() || end.IsZero() || end.Before(start) {
			continue
		}
		a.WorkTimer.Known = true
		a.WorkTimer.ElapsedNS += int64(end.Sub(start))
	}
}

// WorkTimer accumulates observed work intervals, never idle gaps.
// Source: apps/mekugi/internal/router/active_work_timer.go@[219eb509]
type WorkTimer struct {
	ElapsedNS int64     `json:"elapsed_ns,omitzero"`
	Since     time.Time `json:"since,omitzero"`
	Known     bool      `json:"known,omitzero"`
}

func (timer WorkTimer) At(now time.Time) time.Duration {
	if !timer.Since.IsZero() {
		return time.Duration(timer.ElapsedNS) + max(0, now.Sub(timer.Since))
	}
	return time.Duration(timer.ElapsedNS)
}

func (timer *WorkTimer) update(running bool, now time.Time) {
	timer.ElapsedNS = int64(timer.At(now))
	timer.Since = time.Time{}
	if running {
		timer.Known = true
		timer.Since = now
	}
}
