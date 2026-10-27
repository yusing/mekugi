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
		if member.HostRunning && member.Turn == caller.Turn {
			return unchanged
		}
		if member.HostRunning {
			return errors.New("request overlaps another host turn")
		}
		if root {
			if member.Wait != nil && (member.Wait.State == "registered" || member.Wait.State == "queued") {
				cancelSubscription(tree, member)
			}
			if !member.Reserved() {
				member.Generation++
			}
		} else if !member.Reserved() || member.Status == Paused {
			return errors.New("child request has no active logical admission")
		}
		member.Stop = nil
		member.start(caller.Turn)
		return nil
	})
}

func (member *Member) start(turn string) {
	member.Turn, member.Status, member.HostRunning, member.LastAnswer = turn, Working, true, ""
}

func (s *Service) AnswerCompleted(ctx context.Context, thread, turn, text string) error {
	return s.update(ctx, thread, func(_ *Tree, member *Member) error {
		if !member.HostRunning || member.Turn != turn {
			return unchanged
		}
		member.LastAnswer = text
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
	return s.update(ctx, thread, func(_ *Tree, member *Member) error {
		stop := member.Stop
		if !member.HostRunning || member.Turn != turn || stop == nil || stop.Turn != turn || stop.Generation != member.Generation {
			return unchanged
		}
		member.HostRunning = false
		return nil
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
		if member.Turn != turn || !member.HostRunning {
			return unchanged
		}
		if status != Completed && status != Failed && status != Interrupted {
			return errors.New("unknown terminal host outcome")
		}
		member.HostRunning = false
		for i := range tree.Effects {
			effect := &tree.Effects[i]
			if effect.Target == thread && effect.Kind == "interrupt" && effect.Turn == turn && hostOwned(effect.State) {
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
				id := fmt.Sprintf("%s:%d:%s:recovery", thread, member.Generation, turn)
				wait := &Wait{Effect: Effect{ID: id, Caller: thread, Target: thread, Generation: member.Generation, Turn: turn, CallerTurn: turn, Kind: "wake", State: "registered"}, Targets: targets, All: true, Recovery: recovery, Stop: "committed"}
				if member.Wait != nil && hostOwned(member.Wait.State) {
					// The preceding wake still owns its dispatch/reconciliation identity.
					member.RecoveryWait = wait
				} else {
					cancelSubscription(tree, member)
					member.Wait = wait
				}
				member.Stop = &LocalStop{Generation: member.Generation, Turn: turn, Wait: id}
				member.Status = Waiting
				return nil
			}
		}
		if stop := member.Stop; status == Completed && stop != nil && stop.Turn == turn && stop.Generation == member.Generation {
			member.Status = Working
			if member.Wait != nil && member.Wait.State == "registered" {
				member.Status = Waiting
			}
			return nil
		}
		if status == Completed {
			if stop := tree.completionStop(member); stop != nil {
				member.Stop = stop
				member.Status = Working
				tree.Effects = append(tree.Effects, Effect{ID: stop.Wait, Caller: thread, CallerTurn: turn, Target: thread, Kind: "completion", State: "pending", Generation: member.Generation})
				return nil
			}
			failure.Message = member.LastAnswer
		}
		tree.finish(member, status, failure.Message)
		return nil
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
		if member.Wait != nil && hostOwned(member.Wait.State) {
			return errors.New("wait continuation requires host reconciliation before new input")
		}
		// Main already owns ordinary host work. Its human input only supersedes
		// logical waiting, without acquiring a second retained admission ledger.
		if member.Parent == "" {
			changed := member.Stop != nil
			tree.cancelCompletion(member)
			if member.Wait != nil && member.Wait.State != "canceled" && member.Wait.State != "completed" {
				cancelSubscription(tree, member)
				changed = true
			}
			if !changed {
				return unchanged
			}
			member.Stop = nil
			if member.Status == Waiting {
				member.Status = Working
			}
			return nil
		}
		tree.cancelCompletion(member)
		previous := member.Status
		if !member.Reserved() || member.Status == Paused {
			if !member.Reserved() {
				member.Generation++
			}
			member.Status = Pending
		}
		if member.Wait != nil && member.Wait.State != "canceled" && member.Wait.State != "completed" {
			cancelSubscription(tree, member)
		}
		member.Stop = nil
		if member.Status == Waiting {
			member.Status = Working
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
		if (member.Wait == nil || member.Wait.ID != wait) && (member.Stop == nil || member.Stop.Wait != wait) || !member.Reserved() && !member.HostRunning {
			return unchanged
		}
		if member.Wait != nil && hostOwned(member.Wait.State) {
			return errors.New("wait continuation requires host reconciliation before interruption")
		}
		cancelSubscription(tree, member)
		tree.cancelCompletion(member)
		member.Stop = nil
		if member.HostRunning {
			turn = member.Turn
		} else if member.Reserved() {
			tree.finish(member, Interrupted, "")
		}
		return nil
	})
	return
}
