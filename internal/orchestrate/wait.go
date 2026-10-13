package orchestrate

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"slices"
	"time"
)

// WaitEvent remains retained independently of a transport request. Prompt
// events describe live host requests, which recovery does not revive.
type WaitEvent struct {
	ID     string         `json:"id"`
	Task   string         `json:"task"`
	Data   jsontext.Value `json:"data"`
	Owner  string         `json:"owner,omitempty"`
	Prompt bool           `json:"prompt,omitzero"`
}

type Wait struct {
	ID        string         `json:"id"`
	Targets   []string       `json:"targets,omitempty"`
	TimeoutMS int            `json:"timeout_ms,omitzero"`
	Deadline  time.Time      `json:"deadline,omitzero"`
	State     string         `json:"state"`
	EventID   string         `json:"event_id,omitempty"`
	Event     jsontext.Value `json:"event,omitempty"`
	TimedOut  bool           `json:"timed_out,omitzero"`
	Error     string         `json:"error,omitempty"`
}

type WaitState struct {
	Events        []WaitEvent       `json:"events,omitempty"`
	Cancellations map[string]string `json:"cancellations,omitempty"`
	Waits         []Wait            `json:"waits,omitempty"`
}

// RegisterWait retains the call result before returning it. A disconnected caller
// can repeat its call identity to recover the same result, including an event.
func (s *Store) RegisterWait(ctx context.Context, workspace, main string, input Wait, now time.Time) (result Wait, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		if input.ID == "" || input.TimeoutMS < 0 || input.TimeoutMS > 1500000 {
			return errors.New("invalid wait identity or deadline")
		}
		if _, exists := m.Wait.Cancellations[input.ID]; exists {
			return errors.New("wait call already used for cancellation")
		}
		slices.Sort(input.Targets)
		input.Targets = slices.Compact(input.Targets)
		for _, w := range m.Wait.Waits {
			if w.ID != input.ID {
				continue
			}
			if !slices.Equal(w.Targets, input.Targets) || w.TimeoutMS != input.TimeoutMS {
				return errors.New("wait call already has different input")
			}
			result = w
			return nil
		}
		for _, target := range input.Targets {
			if !slices.ContainsFunc(m.Batches, func(b Batch) bool { return b.TaskName == target }) {
				return errors.New("wait target is not a retained batch")
			}
		}
		input.State = "registered"
		if input.TimeoutMS != 0 {
			input.Deadline = now.Add(time.Duration(input.TimeoutMS) * time.Millisecond)
		}
		// Earlier logical subscriptions have first claim on retained events.
		resolveWaits(&m.Wait, now)
		if selectWaitEvent(&m.Wait, &input) {
			// The retained reply carries the event for repeated calls.
			input.State = "completed"
			releaseWaitEvent(&m.Wait, input.ID, false)
		}
		m.Wait.Waits = append(m.Wait.Waits, input)
		result = input
		return s.save(m, path)
	})
	return
}

func selectWaitEvent(state *WaitState, w *Wait) bool {
	for i := range state.Events {
		e := &state.Events[i]
		if e.Owner != "" || len(w.Targets) != 0 && !slices.Contains(w.Targets, e.Task) {
			continue
		}
		e.Owner = w.ID
		w.EventID, w.Event = e.ID, slices.Clone(e.Data)
		return true
	}
	return false
}

// releaseWaitEvent returns an owned event to the queue, or drops it once its
// wait has settled with the event.
func releaseWaitEvent(state *WaitState, id string, requeue bool) {
	if !requeue {
		state.Events = slices.DeleteFunc(state.Events, func(e WaitEvent) bool { return e.Owner == id })
		return
	}
	for i := range state.Events {
		if state.Events[i].Owner == id {
			state.Events[i].Owner = ""
		}
	}
}

func resolveWaits(state *WaitState, now time.Time) bool {
	changed := false
	for i := range state.Waits {
		w := &state.Waits[i]
		if w.State != "registered" {
			continue
		}
		if !w.Deadline.IsZero() && !now.Before(w.Deadline) {
			w.State, w.TimedOut, changed = "ready", true, true
		} else if selectWaitEvent(state, w) {
			w.State, changed = "ready", true
		}
	}
	return changed
}

// AdvanceWaits publishes observations and resolves logical deadlines. It never
// repeats a host effect. Ready results need a separate durable dispatch claim.
func (s *Store) AdvanceWaits(ctx context.Context, workspace, main string, events []WaitEvent, now time.Time) (ready []Wait, deadline time.Time, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		changed := false
		for _, e := range events {
			if e.ID == "" || !e.Data.IsValid() {
				return errors.New("invalid wait event")
			}
			if slices.ContainsFunc(m.Wait.Events, func(old WaitEvent) bool { return old.ID == e.ID }) {
				continue
			}
			e.Owner = ""
			m.Wait.Events = append(m.Wait.Events, e)
			changed = true
		}
		changed = resolveWaits(&m.Wait, now) || changed
		for _, w := range m.Wait.Waits {
			if w.State == "ready" {
				ready = append(ready, w)
			}
			if w.State == "registered" && !w.Deadline.IsZero() && (deadline.IsZero() || w.Deadline.Before(deadline)) {
				deadline = w.Deadline
			}
		}
		if changed {
			return s.save(m, path)
		}
		return nil
	})
	return
}

func (s *Store) CancelWait(ctx context.Context, workspace, main, callID, id string) (result Wait, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		if callID == "" {
			return errors.New("cancellation requires call identity")
		}
		target, exists := m.Wait.Cancellations[callID]
		if exists && target != id {
			return errors.New("cancellation call already has different input")
		}
		if slices.ContainsFunc(m.Wait.Waits, func(w Wait) bool { return w.ID == callID }) {
			return errors.New("wait call already used for registration")
		}
		if m.Wait.Cancellations == nil {
			m.Wait.Cancellations = make(map[string]string)
		}
		m.Wait.Cancellations[callID] = id

		for i := range m.Wait.Waits {
			w := &m.Wait.Waits[i]
			if w.ID != id {
				continue
			}
			if w.State == "dispatching" {
				return errors.New("wait delivery is in progress")
			}
			// Canceling an unconfirmed delivery is Main's statement that the
			// result is absent from its history, so its event is queued again.
			if w.State == "registered" || w.State == "ready" || w.State == "rejected" || w.State == "uncertain" {
				releaseWaitEvent(&m.Wait, id, true)
				w.State, w.EventID, w.Event, w.TimedOut, w.Error = "canceled", "", nil, false, ""
				result = *w
				return s.save(m, path)
			}
			result = *w
			if exists {
				return nil
			}
			return s.save(m, path)
		}
		return errors.New("unknown wait identity")
	})
	return
}

// ClaimWaitDelivery prevents both concurrent routers and recovery from blindly
// repeating an injection whose acknowledgement may have been lost.
func (s *Store) ClaimWaitDelivery(ctx context.Context, workspace, main, id string) (result Wait, dispatch bool, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		for i := range m.Wait.Waits {
			w := &m.Wait.Waits[i]
			if w.ID != id {
				continue
			}
			if w.State != "ready" {
				result = *w
				return nil
			}
			w.State = "dispatching"
			result = *w
			err := s.save(m, path)
			dispatch = err == nil
			return err
		}
		return errors.New("unknown wait identity")
	})
	return
}

func (s *Store) RecordWaitDelivery(ctx context.Context, workspace, main, id, state, message string) error {
	// ready is permitted only when the dispatcher confirms no host call was attempted.
	if state != "ready" && state != "delivered" && state != "rejected" && state != "uncertain" {
		return errors.New("invalid wait delivery outcome")
	}
	return s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		for i := range m.Wait.Waits {
			w := &m.Wait.Waits[i]
			if w.ID != id {
				continue
			}
			// A late host acknowledgement also settles a delivery that
			// recovery marked uncertain.
			if w.State != "dispatching" && (w.State != "uncertain" || state == "ready") {
				if w.State == state && w.Error == message {
					return nil
				}
				return errors.New("wait delivery outcome changed")
			}
			w.State, w.Error = state, message
			if state == "delivered" {
				// Main holds the result; repeated calls report delivery only.
				releaseWaitEvent(&m.Wait, id, false)
				w.Event = nil
			}
			return s.save(m, path)
		}
		return errors.New("unknown wait identity")
	})
}

// RecoverWaits runs before a restored run delivers results. Deliveries the
// previous process started become uncertain, and unclaimed prompt events are
// dropped because replay does not revive their host requests.
func (s *Store) RecoverWaits(ctx context.Context, workspace, main string) error {
	return s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		changed := false
		for i := range m.Wait.Waits {
			if w := &m.Wait.Waits[i]; w.State == "dispatching" {
				w.State, w.Error, changed = "uncertain", "delivery was interrupted by restart", true
			}
		}
		n := len(m.Wait.Events)
		m.Wait.Events = slices.DeleteFunc(m.Wait.Events, func(e WaitEvent) bool { return e.Prompt && e.Owner == "" })
		if changed || n != len(m.Wait.Events) {
			return s.save(m, path)
		}
		return nil
	})
}

// UnsettledWaits lists deliveries that need Main's reconciliation.
func (s *Store) UnsettledWaits(ctx context.Context, workspace, main string) (waits []Wait, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, _ string) error {
		for _, w := range m.Wait.Waits {
			if w.State == "uncertain" || w.State == "rejected" {
				waits = append(waits, w)
			}
		}
		return nil
	})
	return
}
