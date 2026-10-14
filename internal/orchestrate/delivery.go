package orchestrate

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"slices"
	"strings"
)

// Delivery records one host call. Dispatching is intent, not confirmed input.
type Delivery struct {
	ReviewWaiver string `json:"review_waiver,omitempty"`
	ReviewScope  string `json:"review_scope,omitempty"`
	ID           string `json:"id"`
	From         string `json:"from"`
	Target       string `json:"target"`
	Message      string `json:"message"`
	State        string `json:"state"`
	TurnID       string `json:"turn_id,omitempty"`
	Error        string `json:"error,omitempty"`
	Deferred     bool   `json:"deferred,omitzero"`
	Dispatch     string `json:"dispatch,omitempty"`
	// Observation settles host effects without claiming that this input was delivered.
	ObservedTurnID string `json:"observed_turn_id,omitempty"`
	ObservedStatus string `json:"observed_status,omitempty"`
}

func (d Delivery) unsettled() bool {
	return (d.State == "dispatching" || d.State == "uncertain") && (d.ObservedTurnID == "" || d.ObservedStatus != "completed" && d.ObservedStatus != "failed" && d.ObservedStatus != "interrupted")
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
	err = s.withDeliveryRun(ctx, workspace, main, input.From, input.Target, "", func(m *manifest, path string) error {
		for _, d := range m.Deliveries {
			if d.ID == input.ID {
				if d.From != input.From || d.Target != input.Target || d.Message != input.Message || d.Deferred != input.Deferred || d.ReviewWaiver != input.ReviewWaiver || d.ReviewScope != input.ReviewScope {
					return errors.New("delivery call already has different input")
				}
				delivery = d
				return nil
			}
		}
		member := func(thread string) bool {
			return slices.ContainsFunc(m.Batches, func(b Batch) bool {
				return (b.VCS == "" || shadowVCS(b.VCS)) && b.State != "removing" && b.State != "removed" && b.Launch != nil && b.Launch.ThreadID == thread && (b.Launch.TurnID != "" || nextTurn && !input.Deferred && input.TurnID == "" && input.From == main && input.Target == thread && b.Launch.TurnUnsent)
			})
		}
		if input.ID == "" || (input.Message == "" || input.From == input.Target) || input.From != main && !member(input.From) || input.Target != main && !member(input.Target) {
			return errors.New("delivery requires separate confirmed run members")
		}
		for _, b := range m.Batches {
			if b.Launch != nil && b.Launch.ThreadID == input.Target && b.PendingWriteback() {
				return errors.New("finish partial shadow writeback before sending new batch input")
			}
		}
		if input.ReviewWaiver != "" || input.ReviewScope != "" {
			if input.ReviewWaiver != "" && strings.TrimSpace(input.ReviewWaiver) == "" || input.ReviewScope != "" && strings.TrimSpace(input.ReviewScope) == "" || input.From != main || input.Target == main || !nextTurn || input.Deferred || input.TurnID != "" {
				return errors.New("review policy or scope requires a coordinator follow-up to an idle batch and nonblank input")
			}
			i := slices.IndexFunc(m.Batches, func(b Batch) bool { return b.Launch != nil && b.Launch.ThreadID == input.Target })
			b := &m.Batches[i] // The confirmed membership check above establishes this target.
			w, err := workflowFor(b)
			if err != nil {
				return err
			}
			unsentScope := input.ReviewScope != "" && input.ReviewWaiver == "" && b.Launch.TurnUnsent && b.Launch.TurnID == "" && b.Launch.HostStatus == ""
			if !unsentScope && !w.stopped() && w.Phase != PhaseReady {
				return errors.New("interrupt the workflow before changing review policy or scope")
			}
			if !unsentScope && b.Launch.HostStatus != "completed" && b.Launch.HostStatus != "interrupted" && b.Launch.HostStatus != "failed" {
				return errors.New("review policy or scope requires confirmed implementer completion")
			}
			if input.ReviewScope != "" {
				for _, d := range m.Deliveries {
					if d.Target == input.Target && d.unsettled() {
						return errors.New("review scope requires settled deliveries; reconcile unconfirmed host input")
					}
				}
			}
			for _, d := range w.Dispatches {
				if d.State != DispatchQueued && d.State != DispatchFailed && (d.State != DispatchAcknowledged || d.HostStatus == "") {
					return errors.New("review policy or scope requires settled host dispatches")
				}
			}
			if input.ReviewWaiver != "" {
				w.ReviewWaiver = input.ReviewWaiver
			}
			if input.ReviewScope != "" && input.ReviewScope != w.Assignment.Scope {
				// The coordinator's amendment and its delivery intent commit together.
				// Host acknowledgement records message delivery, not scope authority.
				w.Assignment.Scope = input.ReviewScope
				w.PreviousReviews, w.Checks = nil, nil
				if w.ReviewWaiver == "" {
					w.Reviews = nil
				}
				w.Outcome = nil
				b.Integration = nil
				w.Phase, w.Reason = PhaseNeedsDecision, "review scope amended; awaiting implementer input"
				if unsentScope {
					w.Phase, w.Reason = PhaseImplementation, ""
				}
			}
		}
		queue := input.Deferred && input.TurnID == ""
		input.ObservedTurnID, input.ObservedStatus = input.TurnID, ""
		if input.TurnID != "" {
			input.ObservedStatus = "running" // The runtime supplied the active recipient turn.
		}
		input.State, input.TurnID, input.Error = "dispatching", "", ""
		if input.Target == main {
			// Child reports are attributed observations, never user input.
			i := slices.IndexFunc(m.Batches, func(b Batch) bool { return b.Launch != nil && b.Launch.ThreadID == input.From })
			task, id := m.Batches[i].TaskName, "message:"+input.ID
			data, err := json.Marshal(map[string]string{
				"event_id": id, "task_name": task, "thread_id": input.From, "kind": "message", "message": input.Message,
			})
			if err != nil {
				return err
			}
			m.Wait.Events = append(m.Wait.Events, WaitEvent{ID: id, Task: task, Data: data})
			input.State = "reported"
			m.Deliveries = append(m.Deliveries, input)
			delivery = input
			err = s.save(m, path)
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
					d.ObservedTurnID, d.ObservedStatus = "", ""
					parts = append(parts, d.Message)
				}
			}
		}
		parts = append(parts, input.Message)
		for i := range m.Batches {
			b := &m.Batches[i]
			if b.Launch != nil && b.Launch.ThreadID == input.Target {
				if input.ObservedTurnID != "" {
					b.Launch.DeliveryTurnID = input.ObservedTurnID
				}
				// After an amendment, idle continuations carry current authority even
				// when its earlier delivery was canceled or rejected. Messages are not replayed.
				if nextTurn && b.Workflow != nil && (input.ReviewScope != "" || slices.ContainsFunc(m.Deliveries, func(d Delivery) bool { return d.Target == input.Target && d.ReviewScope != "" })) {
					parts = append(parts, "Coordinator review scope (replaces the previous accepted scope):\n"+b.Workflow.Assignment.Scope)
				}
				if b.Launch.TurnUnsent {
					for _, evidence := range b.Evidence {
						if err := validateEvidence(evidence); err != nil {
							return err
						}
					}
				}
				b.Launch.TurnUnsent = false // Intent may reach the host; no automatic retry.
			}
		}
		text = strings.Join(parts, "\n")
		m.Deliveries = append(m.Deliveries, input)
		delivery = input
		err := s.save(m, path)
		dispatch = err == nil && !queue
		return err
	})
	return
}

// ObserveDeliveryTurn consumes authenticated live lifecycle or exact-turn history.
// Only a new live start after intent can bind an uncertain delivery. A terminal
// observation must match that retained turn; old or unidentified turns cannot settle it.
func (s *Store) ObserveDeliveryTurn(ctx context.Context, workspace, main, name, turn, status string) error {
	if turn == "" {
		return nil
	}
	if status != "running" && status != "completed" && status != "failed" && status != "interrupted" {
		return errors.New("invalid delivery lifecycle status")
	}
	return s.withBatchRun(ctx, workspace, main, name, func(m *manifest, path string) error {
		i := slices.IndexFunc(m.Batches, func(b Batch) bool { return b.TaskName == name })
		if i < 0 || m.Batches[i].Launch == nil {
			return errors.New("delivery lifecycle requires a launched batch")
		}
		b := &m.Batches[i]
		known := turn == b.Launch.TurnID || slices.ContainsFunc(m.Deliveries, func(d Delivery) bool {
			return d.Target == b.Launch.ThreadID && (d.TurnID == turn || d.ObservedTurnID == turn)
		})
		if b.Workflow != nil {
			known = known || slices.ContainsFunc(b.Workflow.Dispatches, func(d WorkflowDispatch) bool { return d.ThreadID == b.Launch.ThreadID && d.TurnID == turn })
		}
		changed := false
		for i := range m.Deliveries {
			d := &m.Deliveries[i]
			if d.Target != b.Launch.ThreadID {
				continue
			}
			if status == "running" {
				if !known && d.unsettled() && d.ObservedTurnID == "" {
					d.ObservedTurnID, d.ObservedStatus = turn, status
					b.Launch.DeliveryTurnID = turn
					changed = true
				}
			} else if d.ObservedTurnID == turn && d.ObservedStatus == "running" {
				d.ObservedStatus = status
				if b.Launch.DeliveryTurnID == turn {
					b.Launch.DeliveryTurnID = ""
				}
				changed = true
			}
		}
		if changed {
			return s.save(m, path)
		}
		return nil
	})
}

func (s *Store) RecordDelivery(ctx context.Context, workspace, main, id, state, turn, message string) (delivery Delivery, err error) {
	if state != "delivered" && state != "rejected" && state != "canceled" && state != "uncertain" && state != "queued" || state == "delivered" && turn == "" {
		return delivery, errors.New("invalid delivery acknowledgement")
	}
	err = s.withDeliveryRun(ctx, workspace, main, "", "", id, func(m *manifest, path string) error {
		for i := range m.Deliveries {
			d := &m.Deliveries[i]
			if d.ID != id {
				continue
			}
			// Only the delivery owner can establish that its original deferred
			// input was not consumed. Never release a claimed or uncertain input.
			if state == "queued" && (!d.Deferred || d.Dispatch != "" || turn != "" || message != "") {
				return errors.New("only unclaimed deferred input can be queued")
			}
			if d.State != "dispatching" {
				if d.State != state || d.TurnID != turn || d.Error != message {
					return errors.New("delivery acknowledgement changed")
				}
				delivery = *d
				return nil
			}
			d.State, d.TurnID, d.Error = state, turn, message
			for j := range m.Batches {
				launch := m.Batches[j].Launch
				if launch != nil && launch.ThreadID == d.Target && launch.TurnID == "" {
					if state == "delivered" {
						launch.TurnID = turn
					}
					launch.TurnUnsent = (state == "canceled" || state == "rejected") && launch.HostStatus == ""
				}
			}
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
