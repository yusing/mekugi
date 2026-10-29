package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// RequestStarted authenticates a new turn or reuses an admitted provider retry.
// Root starts are explicit session-controller work; children need a reservation.
func (s *Service) RequestStarted(ctx context.Context, caller Identity, root bool) error {
	return s.update(ctx, caller.Thread, func(tree *Tree, member *Member) error {
		if caller.Session == "" || caller.Session != member.Session || caller.Turn == "" || root != (member.Parent == "") || !member.AudienceAdmitted {
			return errors.New("request has no matching logical audience")
		}
		_, err := tree.transitionHost(member, hostEvent{kind: hostEventStarted, turn: caller.Turn})
		return err
	})
}

func (member *Member) start(turn string) {
	member.Turn, member.Status, member.HostRunning, member.LastAnswer = turn, Working, true, ""
	member.answerItems = nil
}

func (s *Service) AnswerCompleted(ctx context.Context, thread, turn, item, text string) error {
	return s.update(ctx, thread, func(_ *Tree, member *Member) error {
		if !member.HostRunning || member.Turn != turn || text == "" || slices.Contains(member.answerItems, item) {
			return unchanged
		}
		member.answerItems = slices.Concat(member.answerItems, []string{item})
		if member.LastAnswer != "" {
			member.LastAnswer += "\n\n"
		}
		member.LastAnswer += text
		return nil
	})
}

// InputConsumed retires unclaimed notifications already present in the admitted
// parent's model input. Injection acknowledgement alone does not mean they were
// read: messages delivered while the parent is stopped must still wake it.
func (s *Service) InputConsumed(ctx context.Context, caller Identity, items []string) error {
	if len(items) == 0 {
		return nil
	}
	owner, err := s.owner(caller.Thread)
	if errors.Is(err, ErrNotAdmitted) {
		return nil // Unadmitted history has no notification ledger to consume.
	}
	if err != nil {
		return err
	}
	if owner.state.Load().Members[caller.Thread].Wait.CompactionPending() {
		return nil
	}
	var consumed []Effect
	for _, effect := range owner.state.Load().Effects {
		if effect.Target != caller.Thread || effect.Kind != "message" && effect.Kind != "final" || effect.Consumed || effect.ClaimedBy != "" {
			continue
		}
		id := effect.ID
		if effect.Kind == "message" {
			id = effect.Caller + ":" + id
		}
		if slices.Contains(items, MessageItemID(id)) {
			consumed = append(consumed, effect)
		}
	}
	if len(consumed) == 0 {
		return nil
	}
	return s.update(ctx, caller.Thread, func(tree *Tree, member *Member) error {
		if caller.Session == "" || member.Session != caller.Session || !member.AudienceAdmitted {
			return errors.New("consumed input has no matching logical audience")
		}
		changed := false
		for _, candidate := range consumed {
			effect := tree.Effect(candidate.Caller, candidate.ID)
			if effect == nil || effect.Consumed || effect.ClaimedBy != "" {
				continue
			}
			effect.Consumed, changed = true, true
		}
		if !changed {
			return unchanged
		}
		return nil
	})
}

// CheckpointWait settles a compaction prerequisite without canceling the exact
// wait whose enclosing execution already committed its local stop.
func (s *Service) CheckpointWait(ctx context.Context, thread, turn string) error {
	return s.update(ctx, thread, func(tree *Tree, member *Member) error {
		_, err := tree.transitionHost(member, hostEvent{kind: hostEventCheckpoint, turn: turn})
		return err
	})
}

// activeDescendants follows canonical admitted task ancestry. Pending includes
// an admitted child start that has not yet acquired its host turn.
func (tree *Tree) activeDescendants(member *Member) []string {
	var names []string
	for _, child := range tree.Members {
		if child.activeDescendantOf(member) {
			names = append(names, child.Task)
		}
	}
	slices.Sort(names)
	return names
}

func (member *Member) activeDescendantOf(parent *Member) bool {
	return strings.HasPrefix(member.Task, parent.Task+"/") && (member.Status == Pending || member.Status == Working || member.Status == Waiting)
}

func (tree *Tree) completionStop(member *Member) *LocalStop {
	if len(tree.activeDescendants(member)) == 0 {
		return nil
	}
	return &LocalStop{Generation: member.Generation, Turn: member.Turn, Wait: fmt.Sprintf("%s:%d:%s:completion", member.Thread, member.Generation, member.Turn)}
}

func (tree *Tree) cancelCompletion(member *Member) {
	if member.Stop == nil {
		return
	}
	effect := tree.Effect(member.Thread, member.Stop.Wait)
	if effect == nil || effect.Kind != "completion" || effect.State != "pending" {
		return
	}
	effect.State = "canceled"
}

func (s *Service) HostCompleted(ctx context.Context, thread, turn string, status Status, failure HostFailure) error {
	return s.update(ctx, thread, func(tree *Tree, member *Member) error {
		_, err := tree.transitionHost(member, hostEvent{kind: hostEventTerminal, turn: turn, status: status, failure: failure})
		return err
	})
}

func (tree *Tree) finish(member *Member, status Status, text string) {
	tree.cancelCompletion(member)
	member.Status = status
	if member.Wait != nil && member.Wait.State != "completed" && !hostOwned(member.Wait.State) {
		cancelSubscription(tree, member)
	}
	if member.Parent != "" {
		tree.Effects = append(tree.Effects, Effect{ID: fmt.Sprintf("%s:%d:%s:final", member.Thread, member.Generation, member.Turn), Caller: member.Thread, Target: member.Parent, CallerTurn: member.Turn, CallerGeneration: member.Generation, Kind: "final", State: "pending", Message: text, Outcome: status})
	}
}

// Pause retires one runtime, or its whole tree when the runtime is the root.
func (s *Service) Pause(ctx context.Context, thread string) error {
	return s.update(ctx, thread, func(tree *Tree, member *Member) error {
		if member.Parent == "" {
			thread = ""
		}
		pause(tree, thread)
		return nil
	})
}

func pause(tree *Tree, thread string) {
	for id := range tree.Members {
		if thread != "" && thread != id {
			continue
		}
		member := tree.edit(id)
		if member.Reserved() {
			member.Status = Paused
		}
		member.HostRunning, member.Turn = false, ""
		if member.Wait != nil {
			member.Wait.State = pausedState(member.Wait.State)
			if compact := member.Wait.Compaction; compact != nil {
				compact.State = pausedState(compact.State)
			}
		}
		if member.RecoveryWait != nil {
			member.RecoveryWait.State = pausedState(member.RecoveryWait.State)
		}
	}
	for i := range tree.Effects {
		effect := &tree.Effects[i]
		if thread == "" || effect.Caller == thread || effect.Target == thread {
			effect.State = pausedState(effect.State)
		}
	}
}

func pausedState(state string) string {
	switch state {
	case "dispatching":
		return "uncertain"
	case "pending", "registered", "queued":
		return "paused"
	}
	return state
}

func hostOwned(state string) bool { return state == "dispatching" || state == "uncertain" }

// HumanInput reserves and claims ordinary composer input atomically with agent
// controls. Its stable input identity never confers an agent caller or message role.
func (s *Service) HumanInput(ctx context.Context, thread, id string) (result *Effect, err error) {
	err = s.update(ctx, thread, func(tree *Tree, member *Member) error {
		if id == "" || !member.AudienceAdmitted {
			return errors.New("human input requires an admitted audience and input identity")
		}
		if old := tree.Effect(thread, id); old != nil {
			return errors.New("human input identity already has a retained outcome")
		}
		// Main already owns ordinary host work. Its human input only supersedes
		// logical waiting, without acquiring a second retained admission ledger.
		if member.Parent == "" {
			_, err := tree.transitionHost(member, hostEvent{kind: hostEventHumanInput})
			return err
		}
		previous := member.Status
		if !member.Reserved() || member.Status == Paused {
			if !member.Reserved() {
				member.Generation++
			}
			member.Status = Pending
		}
		if _, err := tree.transitionHost(member, hostEvent{kind: hostEventHumanInput}); err != nil {
			return err
		}
		tree.Effects = append(tree.Effects, Effect{ID: id, Caller: thread, Target: thread, Kind: "human", State: "pending", Generation: member.Generation, PreviousStatus: previous})
		result, err = tree.claimEffect(thread, id)
		if err == nil && result == nil {
			err = errors.New("another control operation requires settlement before human input")
		}
		return err
	})
	if err != nil {
		result = nil
	}
	return
}

// HumanInputCommitted confirms only the host's stable composer identity. Text
// resemblance and an unrelated turn's start cannot settle uncertain human input.
func (s *Service) HumanInputCommitted(ctx context.Context, thread, turn, id string) error {
	return s.update(ctx, thread, func(tree *Tree, member *Member) error {
		effect := tree.Effect(thread, id)
		if effect == nil || effect.Kind != "human" || !hostOwned(effect.State) || effect.Generation != member.Generation {
			return unchanged
		}
		if turn == "" || effect.Turn != "" && effect.Turn != turn {
			return errors.New("committed human input has a conflicting host turn")
		}
		effect.State, effect.Turn, effect.Error = "confirmed", turn, ""
		return nil
	})
}

// HumanInterrupt settles an exact logical stop or returns its still-running
// host turn to the sole request owner for interruption.
func (s *Service) HumanInterrupt(ctx context.Context, thread, wait string) (turn string, err error) {
	err = s.update(ctx, thread, func(tree *Tree, member *Member) error {
		if _, err := tree.transitionHost(member, hostEvent{kind: hostEventHumanInterrupt, call: wait}); err != nil {
			return err
		}
		if member.HostRunning {
			turn = member.Turn
		}
		return nil
	})
	return
}

// hostEvent identifies evidence already authenticated by the owning entrypoint.
// turn/item/call bind host work, enclosing executions and subscriptions respectively.
type hostEvent struct {
	kind                           hostEventKind
	turn, item, call, handle       string
	status                         Status
	failure                        HostFailure
	succeeded, compact, standalone bool
}

type hostEventKind uint8

const (
	hostEventStarted hostEventKind = iota
	hostEventTerminal
	hostEventCheckpoint
	hostEventExecution
	hostEventLocalStop
	hostEventWaitRegistration
	hostEventHumanInput
	hostEventHumanInterrupt
	hostEventControl
)

// transitionHost implements SPEC's host-event/logical-state table. It runs only
// inside Service.update, so wait disposition and logical settlement publish together.
// unchanged also tells wait registration to return its existing subscription.
func (tree *Tree) transitionHost(member *Member, event hostEvent) (*LocalStop, error) {
	switch event.kind {
	case hostEventStarted:
		if member.HostRunning && member.Turn == event.turn {
			return nil, unchanged
		}
		if member.HostRunning {
			return nil, errors.New("request overlaps another host turn")
		}
		if member.Parent == "" {
			if member.Wait != nil && (member.Wait.State == "registered" || member.Wait.State == "queued") {
				cancelSubscription(tree, member)
			}
			if !member.Reserved() {
				member.Generation++
			}
		} else if !member.Reserved() || member.Status == Paused {
			return nil, errors.New("child request has no active logical admission")
		}
		member.Stop = nil
		member.start(event.turn)
		return nil, nil
	case hostEventTerminal:
		turn, status, failure := event.turn, event.status, event.failure

		if member.Turn != turn || !member.HostRunning {
			return nil, unchanged
		}
		if status != Completed && status != Failed && status != Interrupted {
			return nil, errors.New("unknown terminal host outcome")
		}
		member.HostRunning = false
		for i := range tree.Effects {
			effect := &tree.Effects[i]
			if effect.Target == member.Thread && effect.Kind == "interrupt" && effect.Turn == turn && hostOwned(effect.State) {
				effect.State, effect.Error = "confirmed", ""
			}
		}
		if status == Failed && failure.Recoverable {
			recovery := make(map[string]uint64)
			var targets []string
			for id, child := range tree.Members {
				if strings.HasPrefix(child.Task, member.Task+"/") {
					targets = append(targets, id)
				}
				if child.activeDescendantOf(member) {
					recovery[id] = child.Generation
				}
			}
			if len(recovery) != 0 {
				slices.Sort(targets)
				tree.cancelCompletion(member)
				id := fmt.Sprintf("%s:%d:%s:recovery", member.Thread, member.Generation, turn)
				wait := &Wait{Effect: Effect{ID: id, Caller: member.Thread, Target: member.Thread, Generation: member.Generation, Turn: turn, CallerTurn: turn, Kind: "wake", State: "registered"}, Targets: targets, All: true, Recovery: recovery, Stop: "committed"}
				if member.Wait != nil && hostOwned(member.Wait.State) {
					// The preceding wake still owns its dispatch/reconciliation identity.
					member.RecoveryWait = wait
				} else {
					cancelSubscription(tree, member)
					member.Wait = wait
				}
				member.Stop = &LocalStop{Generation: member.Generation, Turn: turn, Wait: id}
				member.Status = Waiting
				return nil, nil
			}
		}
		if stop := member.Stop; status == Completed && stop != nil && stop.Turn == turn && stop.Generation == member.Generation {
			member.Status = Working
			if member.Wait != nil && member.Wait.State == "registered" {
				member.Status = Waiting
				if compact := member.Wait.Compaction; compact != nil && compact.State == "registered" {
					compact.State = "pending"
				}
			}
			return nil, nil
		}
		if status == Completed {
			if stop := tree.completionStop(member); stop != nil {
				member.Stop = stop
				member.Status = Working
				tree.Effects = append(tree.Effects, Effect{ID: stop.Wait, Caller: member.Thread, CallerTurn: turn, Target: member.Thread, Kind: "completion", State: "pending", Generation: member.Generation})
				return nil, nil
			}
			failure.Message = member.LastAnswer
		}
		tree.finish(member, status, failure.Message)
		return nil, nil
	case hostEventCheckpoint:
		stop := member.Stop
		if !member.HostRunning || member.Turn != event.turn || stop == nil || stop.Turn != event.turn || stop.Generation != member.Generation {
			return nil, unchanged
		}
		member.HostRunning = false
		return nil, nil
	case hostEventWaitRegistration:
		old := member.Wait
		if old != nil && old.Compaction != nil && hostOwned(old.Compaction.State) && (old.Turn != event.turn || old.Item != event.item) {
			return nil, errors.New("compaction requires native settlement or reconciliation before another checkpoint")
		}
		if old == nil || old.ID != event.call && (old.State == "canceled" || old.State == "completed" || old.State == "paused" || old.State == "timeout") {
			return nil, nil
		}
		if old.Turn != event.turn || old.Item != event.item || old.Generation != member.Generation {
			return nil, errors.New("pending wait belongs to another enclosing execution; new input must supersede it before waiting again")
		}
		if (old.Compaction != nil) != event.compact || old.Standalone != event.standalone {
			return nil, errors.New("conflicting checkpoint in the enclosing execution")
		}
		return nil, unchanged
	case hostEventExecution:
		wait := member.Wait
		if wait == nil || wait.Turn != event.turn || wait.Item != event.item || wait.Generation != member.Generation || wait.State != "registered" {
			return nil, unchanged
		}
		if event.handle != "" {
			if wait.ResumeHandle != "" && wait.ResumeHandle != event.handle {
				return nil, errors.New("conflicting enclosing-cell handle")
			}
			wait.ResumeHandle = event.handle
		}
		if event.succeeded {
			wait.Stop = "eligible"
		} else if event.handle == "" {
			cancelSubscription(tree, member)
			member.Stop = nil
			if member.Status == Waiting {
				member.Status = Working
			}
		}
		return nil, nil
	case hostEventLocalStop:
		wait := member.Wait
		if wait == nil || wait.Stop != "eligible" && wait.Stop != "committed" || wait.State != "registered" || wait.Turn != event.turn || event.item != "" && wait.Item != event.item || wait.Generation != member.Generation || !member.HostRunning {
			return nil, unchanged
		}
		if wait.Stop == "committed" {
			return new(*member.Stop), unchanged
		}
		wait.Stop, member.Status = "committed", Waiting
		member.Stop = &LocalStop{Generation: member.Generation, Turn: event.turn, Item: wait.Item, Wait: wait.ID}
		return new(*member.Stop), nil
	case hostEventHumanInput:
		if member.Wait != nil && hostOwned(member.Wait.State) {
			return nil, errors.New("wait continuation requires host reconciliation before new input")
		}
		changed := member.Stop != nil
		tree.cancelCompletion(member)
		if member.Wait != nil && member.Wait.State != "canceled" && member.Wait.State != "completed" {
			cancelSubscription(tree, member)
			changed = true
		}
		if !changed && member.Parent == "" {
			return nil, unchanged
		}
		member.Stop = nil
		if member.Status == Waiting {
			member.Status = Working
		}
		return nil, nil
	case hostEventHumanInterrupt:
		if (member.Wait == nil || member.Wait.ID != event.call) && (member.Stop == nil || member.Stop.Wait != event.call) || !member.Reserved() && !member.HostRunning {
			return nil, unchanged
		}
		if member.Wait != nil && hostOwned(member.Wait.State) {
			return nil, errors.New("wait continuation requires host reconciliation before interruption")
		}
		cancelSubscription(tree, member)
		tree.cancelCompletion(member)
		member.Stop = nil
		if !member.HostRunning && member.Reserved() {
			tree.finish(member, Interrupted, "")
		}
		return nil, nil
	case hostEventControl:
		tree.cancelCompletion(member)
		if member.Wait != nil {
			if hostOwned(member.Wait.State) {
				return nil, errors.New("wait continuation requires host reconciliation before control")
			}
			cancelSubscription(tree, member)
		}
		return nil, nil
	}
	panic("unknown host event")
}
