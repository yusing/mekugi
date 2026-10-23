package control

import (
	"context"
	"errors"
	"fmt"
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
			if member.Wait != nil && !hostOwned(member.Wait.State) {
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

func (s *Service) HostCompleted(ctx context.Context, thread, turn string, status Status, failure string) error {
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
		if stop := member.Stop; status == Completed && stop != nil && stop.Turn == turn && stop.Generation == member.Generation {
			member.Status = Working
			if member.Wait != nil && member.Wait.State == "registered" {
				member.Status = Waiting
			}
			return nil
		}
		if status == Completed {
			failure = member.LastAnswer
		}
		tree.finish(member, status, failure)
		return nil
	})
}

func (tree *Tree) finish(member *Member, status Status, text string) {
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

// HumanInput invalidates an unsent continuation before the session controller
// starts or steers the host. It does not dispatch or resume logical work.
func (s *Service) HumanInput(ctx context.Context, thread string) error {
	return s.update(ctx, thread, func(tree *Tree, member *Member) error {
		if member.Wait != nil && hostOwned(member.Wait.State) {
			return errors.New("wait continuation requires host reconciliation before new input")
		}
		changed := member.Stop != nil
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
	})
}

// HumanInterrupt settles a stopped logical wait or returns its still-running
// host turn to the sole request owner for interruption.
func (s *Service) HumanInterrupt(ctx context.Context, thread, wait string) (turn string, err error) {
	err = s.update(ctx, thread, func(tree *Tree, member *Member) error {
		if member.Wait == nil || member.Wait.ID != wait || !member.Reserved() && !member.HostRunning {
			return unchanged
		}
		if hostOwned(member.Wait.State) {
			return errors.New("wait continuation requires host reconciliation before interruption")
		}
		cancelSubscription(tree, member)
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
