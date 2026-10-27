package control

// Interruptions is an immutable display projection of exact interrupt operations.
// Terminal outcomes come from retained work, independently of final delivery.
type Interruptions struct {
	tree     *Tree
	outcomes map[interruptKey]Status
}

type interruptKey struct{ caller, id string }

func (p *Interruptions) Outcome(caller, id string) Status {
	if p == nil {
		return ""
	}
	return p.outcomes[interruptKey{caller, id}]
}

// Interruptions skips unchanged state before scanning retained effects.
// A control-state change rebuilds the derived index in linear time.
func (s *Service) Interruptions(thread string, previous *Interruptions) *Interruptions {
	owner, err := s.owner(thread)
	if err != nil {
		return nil
	}
	tree := owner.state.Load()
	if previous != nil && previous.tree == tree {
		return previous
	}
	type work struct {
		thread     string
		generation uint64
	}
	finals := make(map[work]Status)
	for _, effect := range tree.Effects {
		if effect.Kind == "final" {
			finals[work{effect.Caller, effect.CallerGeneration}] = effect.Outcome
		}
	}
	result := &Interruptions{tree: tree, outcomes: make(map[interruptKey]Status)}
	for _, effect := range tree.Effects {
		if effect.Kind == "interrupt" && effect.State == "confirmed" {
			if outcome := finals[work{effect.Target, effect.Generation}]; outcome != "" {
				result.outcomes[interruptKey{effect.Caller, effect.ID}] = outcome
			}
		}
	}
	return result
}
