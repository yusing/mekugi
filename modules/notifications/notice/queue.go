package notice

import (
	"fmt"
	"slices"
	"sync"
)

// Source: internal/router/critical_errors.go:34:457@b4d36aa2 CriticalErrors

type Surface uint8

const (
	Error Surface = iota
	Progress
)

// Input is producer-owned text and identity, never execution authorization.
// Request notices wait for an identified turn's authoritative outcome.
type Input struct {
	ID, Session, Thread, Turn, Category, Reference, Text string
	Surface                                              Surface
	Request                                              bool
}

type entry struct {
	Input
	count, delivered    uint64
	inFlight, awaitTurn bool
}

type turnOutcome struct{ thread, turn, status string }

// Service holds bounded invocation recovery state. A nil service is disabled.
type Service struct {
	mu       sync.Mutex
	entries  []*entry
	outcomes []turnOutcome
	overflow uint64
}

func (s *Service) Submit(input Input) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	awaitTurn := input.Request && input.Thread != "" && input.Turn != ""
	if awaitTurn {
		switch s.outcome(input.Thread, input.Turn) {
		case "completed", "interrupted":
			return
		case "failed":
			awaitTurn = false
		}
	}
	for _, e := range s.entries {
		if input.Request && input.Turn != "" && e.Thread == input.Thread && e.Turn == input.Turn && e.Reference == input.Reference {
			return
		}
	}
	for _, e := range s.entries {
		if e.Session == input.Session && e.Category == input.Category && (input.Request || e.Thread == input.Thread) {
			e.count++
			if input.Request {
				e.Thread, e.Turn, e.Reference, e.awaitTurn = input.Thread, input.Turn, input.Reference, awaitTurn
			}
			return
		}
	}
	if len(s.entries) >= 256 {
		s.overflow++
		return
	}
	s.entries = append(s.entries, &entry{Input: input, count: 1, awaitTurn: awaitTurn})
}

// ObserveTurn reconciles late finalization without retaining live host state.
func (s *Service) ObserveTurn(thread, turn, status string) {
	if s == nil || thread == "" || turn == "" {
		return
	}
	switch status {
	case "completed", "interrupted", "failed":
	default:
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outcome(thread, turn) == "" {
		if len(s.outcomes) == 256 {
			s.outcomes = slices.Delete(s.outcomes, 0, 1)
		}
		s.outcomes = append(s.outcomes, turnOutcome{thread, turn, status})
	}
	s.entries = slices.DeleteFunc(s.entries, func(e *entry) bool {
		if e.Thread != thread || e.Turn != turn {
			return false
		}
		if status == "failed" {
			e.awaitTurn = false
			return false
		}
		return true
	})
}

func (s *Service) outcome(thread, turn string) string {
	for _, o := range s.outcomes {
		if o.thread == thread && o.turn == turn {
			return o.status
		}
	}
	return ""
}

func (e *entry) text() string {
	if e.count > 1 {
		return fmt.Sprintf("%s (occurred %d times)", e.Text, e.count)
	}
	return e.Text
}

// Pending returns launcher recovery after terminal restoration, including repeats
// that arrived after a successful native display.
func (s *Service) Pending() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []string
	for _, e := range s.entries {
		if e.count > e.delivered {
			result = append(result, "mekugi: "+e.text())
		}
	}
	if s.overflow != 0 {
		result = append(result, fmt.Sprintf("mekugi: %d additional failures could not be retained; check the failed turns.", s.overflow))
	}
	return result
}

type Message struct{ ID, Text string }

// Batch keeps its captured repeat counts private; messages are detached values.
type Batch struct {
	Messages []Message
	owner    *Service
	claims   []claim
}

type claim struct {
	entry *entry
	count uint64
}

type Delivery struct{ Errors, Progress Batch }

// Claim receives an application-validated root and descendant-name snapshot.
// Nil descendants still permit the root and invocation-wide auxiliary notices.
func (s *Service) Claim(root string, descendants map[string]string) *Delivery {
	if s == nil || root == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := &Delivery{Errors: Batch{owner: s}, Progress: Batch{owner: s}}
	for _, e := range s.entries {
		if e.awaitTurn || e.delivered != 0 || e.inFlight {
			continue
		}
		name, child := descendants[e.Thread]
		if e.Thread != root && !child && !(e.Thread == "" && e.Session == "") {
			continue
		}
		e.inFlight = true
		text := e.text()
		if e.Thread != root && e.Thread != "" && child {
			text = name + ": " + text
		}
		batch := &d.Errors
		if e.Surface == Progress {
			batch = &d.Progress
		}
		batch.Messages = append(batch.Messages, Message{e.ID, text})
		batch.claims = append(batch.claims, claim{e, e.count})
	}
	if len(d.Errors.Messages)+len(d.Progress.Messages) == 0 {
		return nil
	}
	return d
}

// Finish acknowledges only this surface's actual visible delivery.
func (b *Batch) Finish(visible bool) {
	if b == nil || b.owner == nil {
		return
	}
	b.owner.mu.Lock()
	defer b.owner.mu.Unlock()
	for _, claim := range b.claims {
		claim.entry.inFlight = false
		if visible {
			claim.entry.delivered = claim.count
		}
	}
}
