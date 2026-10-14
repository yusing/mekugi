package orchestrate

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// Delivery records one host call. Dispatching is intent, not confirmed input.
type Delivery struct {
	ReviewWaiver string `json:"review_waiver,omitempty"`
	ID           string `json:"id"`
	From         string `json:"from"`
	Target       string `json:"target"`
	Message      string `json:"message"`
	State        string `json:"state"`
	TurnID       string `json:"turn_id,omitempty"`
	Error        string `json:"error,omitempty"`
	Deferred     bool   `json:"deferred,omitzero"`
	Dispatch     string `json:"dispatch,omitempty"`
}

// BeginDelivery uses input.TurnID to identify an active recipient turn.
// Deferred input is queued only when idle; stored TurnID records acknowledgement.
func (s *Store) BeginDelivery(ctx context.Context, workspace, main string, input Delivery) (delivery Delivery, dispatch bool, err error) {
	delivery, _, dispatch, err = s.beginDelivery(ctx, workspace, main, input, false)
	return
}

// BeginTurnDelivery reserves queued input with the new turn's dispatch intent.
func (s *Store) BeginTurnDelivery(ctx context.Context, workspace, main string, input Delivery) (delivery Delivery, text string, dispatch bool, err error) {
	return s.beginDelivery(ctx, workspace, main, input, true)
}

func (s *Store) beginDelivery(ctx context.Context, workspace, main string, input Delivery, nextTurn bool) (delivery Delivery, text string, dispatch bool, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		for _, d := range m.Deliveries {
			if d.ID == input.ID {
				if d.From != input.From || d.Target != input.Target || d.Message != input.Message || d.Deferred != input.Deferred || d.ReviewWaiver != input.ReviewWaiver {
					return errors.New("delivery call already has different input")
				}
				delivery = d
				return nil
			}
		}
		member := func(thread string) bool {
			return slices.ContainsFunc(m.Batches, func(b Batch) bool {
				return (b.VCS == "" || shadowVCS(b.VCS)) && b.State != "removing" && b.State != "removed" && b.Launch != nil && b.Launch.TurnID != "" && b.Launch.ThreadID == thread
			})
		}
		if input.ID == "" || (input.Message == "" || input.From == input.Target) || input.From != main && !member(input.From) || input.Target != main && !member(input.Target) {
			return errors.New("delivery requires separate confirmed run members")
		}
		if input.ReviewWaiver != "" {
			if strings.TrimSpace(input.ReviewWaiver) == "" || input.From != main || input.Target == main || !nextTurn || input.Deferred || input.TurnID != "" {
				return errors.New("review waiver requires a coordinator follow-up to an idle batch and a user reason")
			}
			i := slices.IndexFunc(m.Batches, func(b Batch) bool { return b.Launch != nil && b.Launch.ThreadID == input.Target })
			b := &m.Batches[i] // The confirmed membership check above establishes this target.
			w, err := workflowFor(b)
			if err != nil {
				return err
			}
			if !w.stopped() && w.Phase != PhaseReady {
				return errors.New("interrupt the workflow before selecting a review waiver")
			}
			if b.Launch.HostStatus != "completed" && b.Launch.HostStatus != "interrupted" && b.Launch.HostStatus != "failed" {
				return errors.New("review waiver requires confirmed implementer completion")
			}
			for _, d := range w.Dispatches {
				if d.State != DispatchQueued && d.State != DispatchFailed && (d.State != DispatchAcknowledged || d.HostStatus == "") {
					return errors.New("review waiver requires settled host dispatches")
				}
			}
			w.ReviewWaiver = input.ReviewWaiver
		}
		queue := input.Deferred && input.TurnID == ""
		input.State, input.TurnID, input.Error = "dispatching", "", ""
		if input.Target == main {
			// Child reports are attributed observations, never user input.
			input.State = "reported"
			m.Deliveries = append(m.Deliveries, input)
			delivery = input
			err := s.save(m, path)
			dispatch = err == nil
			return err
		}
		var parts []string
		if queue {
			input.State = "queued"
		} else if nextTurn {
			for i := range m.Deliveries {
				d := &m.Deliveries[i]
				if d.Target == input.Target && d.State == "queued" {
					d.State, d.Dispatch = "dispatching", input.ID
					parts = append(parts, d.Message)
				}
			}
		}
		parts = append(parts, input.Message)
		text = strings.Join(parts, "\n")
		m.Deliveries = append(m.Deliveries, input)
		delivery = input
		err := s.save(m, path)
		dispatch = err == nil && !queue
		return err
	})
	return
}

func (s *Store) RecordDelivery(ctx context.Context, workspace, main, id, state, turn, message string) (delivery Delivery, err error) {
	if state != "delivered" && state != "rejected" && state != "canceled" && state != "uncertain" || state == "delivered" && turn == "" {
		return delivery, errors.New("invalid delivery acknowledgement")
	}
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		for i := range m.Deliveries {
			d := &m.Deliveries[i]
			if d.ID != id {
				continue
			}
			if d.State != "dispatching" {
				if d.State != state || d.TurnID != turn || d.Error != message {
					return errors.New("delivery acknowledgement changed")
				}
				delivery = *d
				return nil
			}
			d.State, d.TurnID, d.Error = state, turn, message
			delivery = *d
			for j := range m.Deliveries {
				queued := &m.Deliveries[j]
				if queued.Dispatch != id {
					continue
				}
				queued.State, queued.TurnID, queued.Error = state, turn, message
				if state == "rejected" || state == "canceled" {
					queued.State, queued.Dispatch, queued.TurnID, queued.Error = "queued", "", "", ""
				}
			}
			return s.save(m, path)
		}
		return errors.New("delivery has no retained intent")
	})
	return
}
