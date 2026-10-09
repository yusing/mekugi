package orchestrate

import (
	"context"
	"errors"
	"slices"
)

// Delivery records one host call. Dispatching is intent, not confirmed input.
type Delivery struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Target  string `json:"target"`
	Message string `json:"message"`
	State   string `json:"state"`
	TurnID  string `json:"turn_id,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (s *Store) BeginDelivery(ctx context.Context, workspace, main string, input Delivery) (delivery Delivery, dispatch bool, err error) {
	err = s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		for _, d := range m.Deliveries {
			if d.ID == input.ID {
				if d.From != input.From || d.Target != input.Target || d.Message != input.Message {
					return errors.New("delivery call already has different input")
				}
				delivery = d
				return nil
			}
		}
		member := func(thread string) bool {
			return slices.ContainsFunc(m.Batches, func(b Batch) bool {
				return b.State == "launched" && b.Launch != nil && b.Launch.ThreadID == thread
			})
		}
		if input.ID == "" || input.Message == "" || input.From == input.Target || input.From != main && !member(input.From) || !member(input.Target) {
			return errors.New("delivery requires a run member and a separate launched batch target")
		}
		input.State, input.TurnID, input.Error = "dispatching", "", ""
		m.Deliveries = append(m.Deliveries, input)
		delivery = input
		err := s.save(m, path)
		dispatch = err == nil
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
			return s.save(m, path)
		}
		return errors.New("delivery has no retained intent")
	})
	return
}
