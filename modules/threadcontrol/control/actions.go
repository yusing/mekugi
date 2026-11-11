package control

import (
	"context"
	"errors"
	"maps"
	"strings"
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
	return s.PrepareActionWithSources(ctx, caller, target, kind, message, "")
}

// ActionTarget checks the same admission as PrepareAction before asynchronous
// file acquisition. Publication repeats the check after acquisition completes.
func (s *Service) ActionTarget(caller Identity, target, kind, message string) (*Member, error) {
	owner, err := s.owner(caller.Thread)
	if err != nil {
		return nil, err
	}
	receiver, _, err := owner.state.Load().actionTarget(caller, target, kind, message)
	if err != nil {
		return nil, err
	}
	return detach(receiver)
}

func (tree *Tree) actionTarget(caller Identity, target, kind, message string) (*Member, *Effect, error) {
	member, err := admitted(tree, caller)
	if err != nil {
		return nil, nil, err
	}
	if caller.Call == "" || (kind != "message" && kind != "followup" && kind != "interrupt") {
		return nil, nil, errors.New("control action requires a host call and known operation")
	}
	receiver, err := resolve(tree, member, target)
	if err != nil {
		return nil, nil, err
	}
	if kind != "message" && receiver.Parent == "" {
		return nil, nil, errors.New("root control belongs to the human session controller")
	}
	if old := tree.Effect(caller.Thread, caller.Call); old != nil {
		if old.CallerTurn != caller.Turn || old.Target != receiver.Thread || old.Kind != kind || old.Message != message {
			return nil, nil, errors.New("conflicting repeated control call")
		}
		return receiver, old, nil
	}
	if kind == "followup" {
		for _, effect := range tree.Effects {
			if effect.Target == receiver.Thread && effect.Kind == "human" && hostOwned(effect.State) {
				return nil, nil, errors.New("human input requires host settlement before follow-up")
			}
		}
	}
	if kind == "message" && receiver.Parent != "" && !receiver.AudienceAdmitted {
		return nil, nil, errors.New("target has not entered the application audience")
	}
	return receiver, nil, nil
}

func (s *Service) PrepareActionWithSources(ctx context.Context, caller Identity, target, kind, message, sources string) (result Effect, err error) {
	created := false // A repeated call reuses its action.
	err = s.update(ctx, caller.Thread, func(tree *Tree, member *Member) error {
		receiver, old, err := tree.actionTarget(caller, target, kind, message)
		if err != nil {
			return err
		}
		if old != nil {
			if old.Sources != sources {
				return errors.New("conflicting repeated control call")
			}
			result = *old
			return unchanged
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
		receiver = tree.edit(receiver.Thread)
		if kind != "message" {
			if _, err := tree.transitionHost(receiver, hostEvent{kind: hostEventControl}); err != nil {
				return err
			}
			if kind == "followup" && (!receiver.Reserved() || receiver.Status == Paused) {
				if !receiver.Reserved() {
					receiver.Generation++
				}
				receiver.Status, receiver.Stop = Pending, nil
			}
		}
		result = Effect{ID: caller.Call, Caller: caller.Thread, CallerTurn: caller.Turn, CallerGeneration: member.Generation, Target: receiver.Thread, Kind: kind, Message: message, State: "pending", Generation: receiver.Generation, PreviousStatus: receiver.Status}
		result.Sources = sources
		tree.Effects = append(tree.Effects, result)
		created = true
		return nil
	})
	if err == nil && created && kind == "message" {
		s.RecordFeature(ctx, caller.Thread, caller.Call, FeatureEvent{Kind: FeatureMessageAccepted, Sources: sources != ""})
	}
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
	if effect.Kind == "summary" {
		wait := tree.Members[caller].Wait
		if wait.State != "registered" || !wait.Yielded {
			effect.State = "canceled"
			return nil, nil
		}
		tree.resolveStoppedSummaries(wait)
		if effect.State == "failed" {
			return nil, nil
		}
	}
	if effect.Kind == "interrupt" {
		if effect.Generation != member.Generation {
			// The captured work has already settled. A delayed claim must not
			// cancel or interrupt the target's successor generation.
			effect.State = "confirmed"
			return nil, nil
		}
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
	if member != nil && member.Wait.CompactionPending() && effect.Kind != "compact" && effect.Kind != "interrupt" &&
		!((effect.Kind == "human" || effect.Kind == "followup") && member.HostRunning && member.Wait.NativeCell != "") {
		return nil, unchanged
	}
	blocked := unchanged
	if effect.Kind == "interrupt" {
		// Interruption may already have canceled undispatched work above.
		blocked = nil
	}
	if member != nil && member.Wait != nil && effect.Kind != "wake" && hostOwned(member.Wait.State) {
		return nil, blocked
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
		if hostOwned(other.State) || before && other.State == "pending" && effect.Kind != "compact" {
			return nil, blocked
		}
	}
	switch effectStep(*effect) {
	case "compact":
		if member.Wait != nil && member.Wait.NativeCell != "" {
			return nil, unchanged // The native cell owns reset acceptance and settlement.
		}
		if member.HostRunning || member.Wait == nil || member.Wait.State != "registered" || member.Wait.Stop != "committed" || member.Generation != effect.Generation {
			return nil, unchanged
		}
	case "create", "assignment_input", "admit", "message", "final", "summary":
	case "completion":
		if member.HostRunning || member.Status != Working || member.Generation != effect.Generation || member.Turn != effect.CallerTurn || member.Stop == nil || member.Stop.Wait != effect.ID {
			effect.State = "canceled"
			return nil, nil
		}
		names := tree.activeDescendants(member)
		if len(names) == 0 {
			effect.State = "canceled"
			tree.finish(member, Completed, member.LastAnswer)
			return nil, nil
		}
		effect.Message = strings.Join(names, ", ")
	case "wake":
		if member.Status != Waiting || member.HostRunning || member.Generation != effect.Generation {
			effect.State = "canceled"
			return nil, nil
		}
		// Preceding injections may have settled after PrepareWake queued a
		// progress wake. Preserve their ordinary outcome/message precedence.
		if member.Wait.Yielded && tree.selectWaitOutcome(member.Wait) {
			member.Wait.Yielded, member.Wait.Progress, member.Wait.YieldSeen, member.Wait.Summaries = false, nil, nil, nil
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
	var feature *FeatureEvent
	err := s.update(ctx, caller, func(tree *Tree, _ *Member) error {
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
			if step == "compact" && !receipt.Uncertain {
				feature = &FeatureEvent{Kind: FeatureSelfResetFailed}
			}
			effect.State = "failed"
			if receipt.Uncertain {
				effect.State = "uncertain"
			}
			if step == "create" {
				effect.Target, effect.Child.Session, effect.Child.Path = receipt.Thread, receipt.Session, receipt.Path
			}
			if member != nil && member.Status != Paused {
				if (effect.Kind == "wake" || effect.Kind == "compact") && member.Reserved() && receipt.Uncertain {
					member.Status = Uncertain
				}
				if effect.Kind == "create" && member.Status == Pending {
					member.Status = Failed
					if receipt.Uncertain {
						member.Status = Uncertain
					}
				}
			}
			if (effect.Kind == "human" || effect.Kind == "followup" || effect.Kind == "completion") && member != nil && member.Generation == effect.Generation && !member.HostRunning && member.Reserved() {
				if receipt.Uncertain {
					member.Status = Uncertain
				} else {
					tree.finish(member, Failed, receipt.Error)
				}
			}
			if !receipt.Uncertain && member != nil {
				member.settleRecovery()
			}
			return nil
		}
		effect.State = "confirmed"
		feature = confirmedFeature(step, *effect)
		switch step {
		case "compact":
			effect.State = "completed"
			if receipt.Reconciled && member.Status == Uncertain && member.Wait.State == "registered" {
				member.Status = Waiting
			}
		case "create":
			if receipt.Thread == "" || receipt.Session == "" || tree.Members[receipt.Thread] != nil {
				return errors.New("invalid independent child identity")
			}
			effect.Target, effect.Child.Session, effect.Child.Path = receipt.Thread, receipt.Session, receipt.Path
			child := effect.Child
			tree.Members[receipt.Thread] = &Member{Thread: receipt.Thread, Session: receipt.Session, Path: receipt.Path, Parent: caller, Task: child.Task, Generation: 1, Status: Pending, Settings: child.Settings, Limits: child.Limits, Preload: child.Preload}
			effect.Phase, effect.State = "assignment_input", "pending"
		case "assignment_input":
			effect.Phase, effect.State = "admit", "pending"
		case "admit":
			member.AudienceAdmitted = true
			effect.Phase, effect.State = "initial", "pending"
			if effect.Outcome == Interrupted {
				effect.State = "canceled"
			}
		case "initial", "followup", "wake", "human", "completion":
			if effect.Kind == "wake" {
				effect.State = "completed"
				if member.Wait.Yielded {
					feature = &FeatureEvent{Kind: FeatureProgressDelivered}
					if member.YieldContext != member.Wait.YieldContext {
						member.YieldSeen = nil
					}
					member.YieldContext = member.Wait.YieldContext
					member.YieldSeen = maps.Clone(member.YieldSeen)
					if member.YieldSeen == nil {
						member.YieldSeen = make(map[string]int)
					}
					maps.Copy(member.YieldSeen, member.Wait.YieldSeen)
				}
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
			if effect.Origin != nil {
				effect.Outcome = effect.Origin.Status
			}
		}
		if effect.Kind == "create" && effect.State == "pending" && member != nil && member.Status == Paused {
			effect.State = "paused"
		}
		if member != nil {
			member.settleRecovery()
		}
		return nil
	})
	if err == nil && feature != nil {
		s.RecordFeature(ctx, caller, id, *feature)
	}
	return err
}

func (member *Member) settleRecovery() {
	if member.RecoveryWait != nil && !hostOwned(member.Wait.State) {
		member.Wait, member.RecoveryWait = member.RecoveryWait, nil
		if member.Status == Uncertain {
			member.Status = Waiting
		}
	}
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
	if effect.Origin != nil {
		event.Source, event.Task, event.Session = effect.Origin.Thread, effect.Origin.Sender(), effect.Origin.Session
	}
	if effect.Kind == "message" {
		event.Kind, event.Message = "message", effect.Message
	}
	if effect.State == "failed" {
		event.Kind = "delivery_failed"
	}
	return event
}
