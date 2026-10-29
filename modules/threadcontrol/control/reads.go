package control

import (
	"slices"
	"strings"
)

// Effect reads one retained intent, including an embedded wait, without copying
// unrelated members, settings or effect payloads.
func (s *Service) Effect(caller, id string) (*Effect, error) {
	owner, err := s.owner(caller)
	if err != nil {
		return nil, err
	}
	tree := owner.state.Load()
	effect := tree.Effect(caller, id)
	if effect == nil {
		return nil, nil
	}
	return tree.detachedEffect(*effect)
}

func (tree *Tree) detachedEffect(effect Effect) (*Effect, error) {
	result, err := detach(effect)
	if err != nil {
		return nil, err
	}
	if member := tree.Members[effect.Caller]; member != nil && member.Wait != nil && member.Wait.ID == effect.ID && effect.Kind == "wake" {
		// Detach persistent facts before deriving a wake. Effect.Wake and the
		// wake's internal identity are deliberately absent from checkpoint JSON.
		wait, err := detach(member.Wait)
		if err != nil {
			return nil, err
		}
		result.Wake = tree.wakeResult(wait)
	}
	return &result, nil
}

type EffectRef struct{ Caller, ID string }

// PendingEffects selects dispatch candidates in retained arrival order. Claims
// remain authoritative; these identities grant no dispatch permission.
func (s *Service) PendingEffects(thread string) ([]EffectRef, bool, error) {
	owner, err := s.owner(thread)
	if err != nil {
		return nil, false, err
	}
	tree := owner.state.Load()
	var pending []EffectRef
	appendPending := func(effect Effect) {
		if (effect.Target == thread || effect.Caller == thread) && (effect.State == "pending" || effect.State == "queued") {
			pending = append(pending, EffectRef{effect.Caller, effect.ID})
		}
	}
	for _, effect := range tree.Effects {
		appendPending(effect)
	}
	for _, member := range tree.Members {
		if member.Wait != nil {
			if member.Wait.Compaction != nil {
				appendPending(*member.Wait.Compaction)
			}
			appendPending(member.Wait.Effect)
		}
	}
	wait := tree.Members[thread].Wait
	return pending, wait != nil && len(wait.Recovery) != 0 && wait.State == "registered", nil
}

// Action contains only the input facts required by dispatch and confirmation.
// Sender, recipient and initial settings come from one immutable publication.
type Action struct {
	Effect            Effect
	Sender, Recipient string
	Settings          *Settings
}

func (tree *Tree) action(effect Effect) (Action, error) {
	action := Action{Effect: effect, Sender: tree.Members[effect.Caller].Task, Recipient: tree.Members[effect.Target].Task}
	if effectStep(effect) == "initial" {
		settings, err := detach(tree.Members[effect.Target].Settings)
		if err != nil {
			return Action{}, err
		}
		action.Settings = &settings
	}
	return action, nil
}

// Action borrows an already detached claimed effect and detaches only its
// selected input settings. Creation does not need a member read.
func (s *Service) Action(effect Effect) (Action, error) {
	owner, err := s.owner(effect.Caller)
	if err != nil {
		return Action{}, err
	}
	return owner.state.Load().action(effect)
}

// UncertainActions selects only this target's retained confirmation inputs.
// It never admits execution or copies unrelated payloads.
func (s *Service) UncertainActions(thread string) ([]Action, error) {
	owner, err := s.owner(thread)
	if err != nil {
		return nil, err
	}
	tree := owner.state.Load()
	var actions []Action
	appendUncertain := func(effect Effect) error {
		if effect.Target != thread || effect.State != "uncertain" {
			return nil
		}
		// Confirmation uses the phase and retained input, not the frozen
		// creation preparation. Initial execution reads current target settings.
		effect.Child = nil
		detached, err := tree.detachedEffect(effect)
		if err != nil {
			return err
		}
		action, err := tree.action(*detached)
		if err != nil {
			return err
		}
		actions = append(actions, action)
		return nil
	}
	for _, effect := range tree.Effects {
		if err := appendUncertain(effect); err != nil {
			return nil, err
		}
	}
	for _, member := range tree.Members {
		if member.Wait != nil {
			if member.Wait.Compaction != nil {
				if err := appendUncertain(*member.Wait.Compaction); err != nil {
					return nil, err
				}
			}
			if err := appendUncertain(member.Wait.Effect); err != nil {
				return nil, err
			}
		}
	}
	return actions, nil
}

// Agents returns detached display rows without serializing frozen settings or
// retained message payloads. Prefix is a canonical logical task path.
func (s *Service) Agents(thread, prefix string) (map[string]any, error) {
	owner, err := s.owner(thread)
	if err != nil {
		return nil, err
	}
	tree := owner.state.Load()
	matches := func(task string) bool {
		return prefix == "" || task == prefix || strings.HasPrefix(task, prefix+"/")
	}
	var agents, effects []map[string]any
	for _, member := range tree.Members {
		if matches(member.Task) {
			var wait map[string]any
			if member.Wait != nil {
				view := *member.Wait
				view.Targets = slices.Clone(view.Targets)
				if view.RequestedMS != nil {
					view.RequestedMS = new(*view.RequestedMS)
				}
				if view.EffectiveMS != nil {
					view.EffectiveMS = new(*view.EffectiveMS)
				}
				if view.Deadline != nil {
					view.Deadline = new(*view.Deadline)
				}
				wait = view.View(tree)
			}
			agents = append(agents, map[string]any{"agent_name": member.Task, "agent_status": member.Status, "thread_id": member.Thread, "generation": member.Generation, "wait": wait})
		}
	}
	slices.SortFunc(agents, func(a, b map[string]any) int {
		return strings.Compare(a["agent_name"].(string), b["agent_name"].(string))
	})
	appendEffect := func(effect Effect) {
		if effect.State == "confirmed" {
			return
		}
		if !matches(tree.Members[effect.Caller].Task) && (tree.Members[effect.Target] == nil || !matches(tree.Members[effect.Target].Task)) && (effect.Child == nil || !matches(effect.Child.Task)) {
			return
		}
		row := map[string]any{"id": effect.ID, "state": effect.PublicState(), "thread_id": effect.Target}
		if effect.Child != nil {
			row["task"] = effect.Child.Task
		}
		effects = append(effects, row)
	}
	for _, effect := range tree.Effects {
		appendEffect(effect)
	}
	for _, member := range tree.Members {
		if member.Wait != nil {
			if member.Wait.Compaction != nil {
				appendEffect(*member.Wait.Compaction)
			}
			appendEffect(member.Wait.Effect)
		}
	}
	return map[string]any{"agents": agents, "effects": effects}, nil
}
