package control

import (
	"context"
	"errors"
)

func (s *Service) Caller(caller Identity) (*Member, error) {
	owner, err := s.owner(caller.Thread)
	if err != nil {
		return nil, err
	}
	member, err := admitted(owner.state.Load(), caller)
	if err != nil {
		return nil, err
	}
	return detach(member)
}

// Target resolves admitted logical identity before target-scoped reconciliation.
func (s *Service) Target(caller Identity, target string) (*Member, error) {
	owner, err := s.owner(caller.Thread)
	if err != nil {
		return nil, err
	}
	tree := owner.state.Load()
	member, err := admitted(tree, caller)
	if err != nil {
		return nil, err
	}
	receiver, err := resolve(tree, member, target)
	if err != nil {
		return nil, err
	}
	return detach(receiver)
}

func (s *Service) PrepareAction(ctx context.Context, caller Identity, target, kind, message string) (result Effect, err error) {
	err = s.update(ctx, caller.Thread, func(tree *Tree, member *Member) error {
		if _, err := admitted(tree, caller); err != nil {
			return err
		}
		if caller.Call == "" || (kind != "message" && kind != "followup" && kind != "interrupt") {
			return errors.New("control action requires a host call and known operation")
		}
		receiver, err := resolve(tree, member, target)
		if err != nil {
			return err
		}
		if kind != "message" && receiver.Parent == "" {
			return errors.New("root control belongs to the human session controller")
		}
		if old := tree.Effect(caller.Thread, caller.Call); old != nil {
			if old.CallerTurn != caller.Turn || old.Target != receiver.Thread || old.Kind != kind || old.Message != message {
				return errors.New("conflicting repeated control call")
			}
			result = *old
			return unchanged
		}
		if kind == "followup" {
			for _, effect := range tree.Effects {
				if effect.Target == receiver.Thread && effect.Kind == "human" && hostOwned(effect.State) {
					return errors.New("human input requires host settlement before follow-up")
				}
			}
		}
		if kind == "followup" && receiver.Status == Paused {
			for i := range tree.Effects {
				creation := &tree.Effects[i]
				if creation.Kind != "create" || creation.Target != receiver.Thread || creation.State != "paused" {
					continue
				}
				switch creation.Phase {
				case "assignment_input", "admit":
					creation.State, creation.Outcome = "pending", Interrupted
				case "initial":
					creation.State = "canceled"
				}
			}
		}
		if kind == "message" && receiver.Parent != "" && !receiver.AudienceAdmitted {
			return errors.New("target has not entered the application audience")
		}
		receiver = tree.edit(receiver.Thread)
		if kind != "message" {
			if receiver.Wait != nil {
				if hostOwned(receiver.Wait.State) {
					return errors.New("wait continuation requires host reconciliation before control")
				}
				cancelSubscription(tree, receiver)
			}
			if kind == "followup" && (!receiver.Reserved() || receiver.Status == Paused) {
				if member.Limits.Concurrency <= 0 || activeCount(tree) >= member.Limits.Concurrency {
					return errors.New("captured host concurrency limit reached")
				}
				if !receiver.Reserved() {
					receiver.Generation++
				}
				receiver.Status, receiver.Stop = Pending, nil
			}
		}
		result = Effect{ID: caller.Call, Caller: caller.Thread, CallerTurn: caller.Turn, CallerGeneration: member.Generation, Target: receiver.Thread, Kind: kind, Message: message, State: "pending", Generation: receiver.Generation, PreviousStatus: receiver.Status}
		tree.Effects = append(tree.Effects, result)
		return nil
	})
	return
}

// Claims serialize target effects. Wait continuations share this protocol without
// another durable action; successful injection alone never starts a receiver.
func (s *Service) ClaimEffect(ctx context.Context, caller, id string) (result *Effect, err error) {
	err = s.update(ctx, caller, func(tree *Tree, _ *Member) error {
		result, err = tree.claimEffect(caller, id)
		return err
	})
	if err == nil && result != nil && result.Child != nil {
		result.Child, err = detach(result.Child)
	}
	return
}

func (tree *Tree) claimEffect(caller, id string) (*Effect, error) {
	effect := tree.Effect(caller, id)
	if effect == nil {
		return nil, errors.New("unknown control intent")
	}
	if effect.State != "pending" && effect.State != "queued" {
		return nil, unchanged
	}
	member := tree.edit(effect.Target)
	effect = tree.Effect(caller, id)
	if effect.Kind == "interrupt" {
		for i := range tree.Effects {
			queued := &tree.Effects[i]
			if queued.Target != effect.Target || queued.Generation != member.Generation || queued.State != "pending" {
				continue
			}
			if queued.Kind == "create" {
				queued.Outcome = Interrupted
			}
			if effectStep(*queued) == "initial" || queued.Kind == "followup" {
				queued.State = "canceled"
			}
		}
	}
	if member != nil && member.Wait != nil && effect.Kind != "wake" && hostOwned(member.Wait.State) {
		return nil, nil
	}
	before := true
	for _, other := range tree.Effects {
		if other.Caller == caller && other.ID == id {
			before = false
			continue
		}
		if effect.Target == "" || other.Target != effect.Target {
			continue
		}
		if effect.Kind == "interrupt" && member.HostRunning && effectStep(other) == "initial" {
			continue
		}
		if hostOwned(other.State) || before && other.State == "pending" {
			return nil, nil
		}
	}
	switch effectStep(*effect) {
	case "create", "assignment_input", "admit", "message", "final":
	case "wake":
		if member.Status != Waiting || member.HostRunning || member.Generation != effect.Generation {
			effect.State = "canceled"
			return nil, nil
		}
	case "initial", "followup", "human":
		if !member.AudienceAdmitted || !member.Reserved() || member.Generation != effect.Generation {
			return nil, errors.New("execution has no active logical audience reservation")
		}
		if member.HostRunning && member.Stop != nil && member.Stop.Turn == member.Turn {
			return nil, unchanged
		}
	case "interrupt":
		if !member.HostRunning {
			effect.State = "confirmed"
			if member.Reserved() {
				tree.finish(member, Interrupted, "")
			}
			return nil, nil
		}
	default:
		return nil, errors.New("unknown effect kind")
	}
	effect.State = "dispatching"
	if member != nil {
		effect.Turn = member.Turn
		if effect.Kind == "human" && !member.HostRunning {
			effect.Turn = ""
		}
	}
	result := new(*effect)
	if effect.Kind == "wake" {
		result.Wake = tree.wakeResult(member.Wait)
	}
	return result, nil
}

func (s *Service) EffectAcknowledged(ctx context.Context, caller, id string, receipt Receipt) error {
	return s.update(ctx, caller, func(tree *Tree, _ *Member) error {
		effect := tree.Effect(caller, id)
		if effect == nil {
			return errors.New("unknown effect acknowledgement")
		}
		member := tree.edit(effect.Target)
		effect = tree.Effect(caller, id)
		step := effectStep(*effect)
		if effect.Kind == "create" && receipt.Phase != effect.Phase {
			return errors.New("creation acknowledgement does not match retained phase")
		}
		if effect.State == "confirmed" || effect.State == "completed" || effect.State == "failed" {
			return unchanged
		}
		if effect.State != "dispatching" && !(effect.State == "uncertain" && receipt.Reconciled && step != "create" && step != "admit" && receipt.Error == "" && !receipt.Uncertain) {
			return errors.New("effect is not dispatched")
		}
		effect.Error = receipt.Error
		if receipt.Uncertain || receipt.Error != "" {
			effect.State = "failed"
			if receipt.Uncertain {
				effect.State = "uncertain"
			}
			if step == "create" {
				effect.Target, effect.Child.Session, effect.Child.Path = receipt.Thread, receipt.Session, receipt.Path
			}
			if member != nil && member.Status != Paused {
				if effect.Kind == "wake" && member.Reserved() && receipt.Uncertain {
					member.Status = Uncertain
				}
				if effect.Kind == "create" && member.Status == Pending {
					member.Status = Failed
					if receipt.Uncertain {
						member.Status = Uncertain
					}
				}
			}
			if (effect.Kind == "human" || effect.Kind == "followup") && member != nil && member.Generation == effect.Generation && !member.HostRunning && member.Reserved() {
				if receipt.Uncertain {
					member.Status = Uncertain
				} else {
					tree.finish(member, Failed, receipt.Error)
				}
			}
			return nil
		}
		effect.State = "confirmed"
		switch step {
		case "create":
			if receipt.Thread == "" || receipt.Session == "" || tree.Members[receipt.Thread] != nil {
				return errors.New("invalid independent child identity")
			}
			effect.Target, effect.Child.Session, effect.Child.Path = receipt.Thread, receipt.Session, receipt.Path
			child := effect.Child
			tree.Members[receipt.Thread] = &Member{Thread: receipt.Thread, Session: receipt.Session, Path: receipt.Path, Parent: caller, Task: child.Task, Generation: 1, Status: Pending, Settings: child.Settings, Limits: child.Limits}
			effect.Phase, effect.State = "assignment_input", "pending"
		case "assignment_input":
			effect.Phase, effect.State = "admit", "pending"
		case "admit":
			member.AudienceAdmitted = true
			effect.Phase, effect.State = "initial", "pending"
			if effect.Outcome == Interrupted {
				effect.State = "canceled"
			}
		case "initial", "followup", "wake", "human":
			if effect.Kind == "wake" {
				effect.State = "completed"
			}
			if receipt.Reconciled {
				break
			}
			if receipt.Turn == "" || member.HostRunning && member.Turn != receipt.Turn {
				return errors.New("effect acknowledgement has missing or conflicting host turn")
			}
			if member.Generation == effect.Generation && member.Reserved() && (member.Turn != receipt.Turn || member.HostRunning) {
				if member.Turn != receipt.Turn {
					member.start(receipt.Turn)
				}
			}
		case "message":
			effect.Outcome = tree.Members[effect.Caller].Status
		}
		if effect.Kind == "create" && effect.State == "pending" && member != nil && member.Status == Paused {
			effect.State = "paused"
		}
		return nil
	})
}

func effectStep(effect Effect) string {
	if effect.Kind == "create" {
		return effect.Phase
	}
	return effect.Kind
}

// Per-target claims serialize delivery, so retained intent order is arrival order.
func (effect Effect) delivered() bool {
	return (effect.Kind == "message" || effect.Kind == "final") && effect.State == "confirmed" || effect.Kind == "final" && effect.State == "failed"
}

func (tree *Tree) event(effect Effect) *Event {
	event := &Event{ID: effect.Caller + ":" + effect.ID + ":event", Recipient: effect.Target, Source: effect.Caller, Task: tree.Members[effect.Caller].Task, Turn: effect.CallerTurn, Generation: effect.CallerGeneration, Kind: string(effect.Outcome), Status: effect.Outcome}
	if effect.Kind == "message" {
		event.Kind, event.Message = "message", effect.Message
	}
	if effect.State == "failed" {
		event.Kind = "delivery_failed"
	}
	return event
}
