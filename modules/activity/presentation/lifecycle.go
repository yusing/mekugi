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
	WaitingFor   string    `json:",omitzero"`
	Wait         *Value    `json:"-"`
	Waiting      bool      `json:",omitzero"`
	Paused       bool      `json:",omitzero"`
	Final        bool      `json:",omitzero"`
	Started      time.Time `json:",omitzero"`
	LastResponse time.Time `json:",omitzero"`
	Turns        uint64    `json:",omitzero"`
}

func (a *AgentLifecycle) ObserveActive(active bool, at time.Time) {
	if a.Waiting {
		return
	}
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
	a.Paused = false
	a.Waiting = false
	a.WorkTimer.update(true, at)
	a.Responding, a.Final = true, false
	if a.Started.IsZero() {
		a.Started = at
	}
	a.Turns++
}

// TurnCompleted receives only a completion admitted for the current turn.
func (a *AgentLifecycle) TurnCompleted(completed bool, at time.Time) {
	if a.Waiting && completed {
		return
	}
	a.Waiting = false
	a.WorkTimer.update(false, at)
	a.Responding, a.LastResponse, a.Final = false, at, completed
}

// SetWaiting applies detached logical work without inventing a host turn.
func (a *AgentLifecycle) SetWaiting(waiting bool, at time.Time) {
	if a.Waiting == waiting {
		return
	}
	a.Waiting, a.Responding, a.Final = waiting, false, false
	a.WorkTimer.update(waiting, at)
}

// Response records provider response age independently of the active work clock.
// Source: apps/mekugi/internal/router/app_server_events.go:335@1bcbdd5a.
func (a *AgentLifecycle) Response(at time.Time) { a.LastResponse = at }

// ResumeActive keeps host-confirmed input admission without reviving a clock.
// Source: apps/mekugi/internal/router/app_server_ui.go:780@1bcbdd5a.
func (a *AgentLifecycle) ResumeActive() { a.Responding = true }

// Restore replaces admitted history without replaying turn transitions.
// Source: apps/mekugi/internal/router/app_server_resume.go:252:254@1bcbdd5a.
func (a *AgentLifecycle) Restore(intervals iter.Seq2[time.Time, time.Time], turns uint64, responding bool) {
	a.RestoreTiming(intervals)
	a.Turns, a.Responding = turns, responding
}

// Source: apps/mekugi/internal/router/app_server_restore_content.go:362@1bcbdd5a.
func (a *AgentLifecycle) RestoreFinal(completed bool) { a.Final = completed }

// RestorePage extends known history; the caller excludes pages superseded by
// live observations. Missing final evidence does not clear an existing outcome.
// Source: apps/mekugi/internal/router/app_server_history_pages.go:372:376@1bcbdd5a.
func (a *AgentLifecycle) RestorePage(intervals iter.Seq2[time.Time, time.Time], turns uint64, final *bool) {
	a.RestoreTiming(intervals)
	a.Turns = max(a.Turns, turns)
	if final != nil {
		a.Final = *final
	}
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
