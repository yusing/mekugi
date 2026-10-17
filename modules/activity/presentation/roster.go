package presentation

import "slices"

// The attached feed owner keeps one observation-ordered roster. Feed models
// receive detached copies; caller labels never replace stable thread identity.
// Source: apps/mekugi/internal/router/app_server_events.go:91:172@29a3a244.
func (s *Session[S, A]) Agent(thread string) *A {
	for i := range s.roster {
		if s.roster[i].ActivityAgent().Thread == thread {
			return &s.roster[i]
		}
	}
	return nil
}

func (s *Session[S, A]) AgentNamed(name string) *A {
	for i := range s.roster {
		if s.roster[i].ActivityAgent().Name == name {
			return &s.roster[i]
		}
	}
	return nil
}

func (s *Session[S, A]) AddAgent(agent A) { s.roster = append(s.roster, agent) }

func (s *Session[S, A]) Agents() []A { return slices.Clone(s.roster) }

// StartRoster establishes the host-selected root without discarding feed rows
// admitted during startup or resetting their sequence.
func (s *Session[S, A]) StartRoster(root A) { s.roster = []A{root} }

func (s *Session[S, A]) NextSequence() uint64 {
	s.sequence++
	return s.sequence
}

func (s *Session[S, A]) AdvanceSequence(floor uint64) { s.sequence = max(s.sequence, floor) }
