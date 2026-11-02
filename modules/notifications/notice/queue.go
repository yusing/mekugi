package notice

import (
	"fmt"
	"slices"
	"sync"
	"time"
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
	occurred            time.Time
}

type turnOutcome struct{ thread, turn, status string }

type requestSuccess struct {
	thread string
	at     time.Time
}

// Service holds bounded invocation recovery state. A nil service is disabled.
type Service struct {
	mu        sync.Mutex
	entries   []*entry
	outcomes  []turnOutcome
	successes []requestSuccess
	overflow  uint64
}

// RequestSucceeded retires older request errors, independently of host turn
// completion, and lets each attached view clear only feedback for this thread.
func (s *Service) RequestSucceeded(thread string, at time.Time) {
	if s == nil || thread == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.successes = slices.DeleteFunc(s.successes, func(success requestSuccess) bool { return success.thread == thread })
	if len(s.successes) == 256 {
		s.successes = slices.Delete(s.successes, 0, 1)
	}
	s.successes = append(s.successes, requestSuccess{thread, at})
	s.entries = slices.DeleteFunc(s.entries, func(e *entry) bool { return e.Request && e.Thread == thread })
}

func (s *Service) SucceededSince(thread string, at time.Time) bool {
	if s == nil || thread == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.ContainsFunc(s.successes, func(success requestSuccess) bool { return success.thread == thread && success.at.After(at) })
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
			e.occurred = time.Now()
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
	s.entries = append(s.entries, &entry{Input: input, count: 1, awaitTurn: awaitTurn, occurred: time.Now()})
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

type Message struct {
	ID, Text, Thread string
	Occurred         time.Time
}

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
		if !e.claimable(root, descendants) {
			continue
		}
		name, child := descendants[e.Thread]
		e.inFlight = true
		text := e.text()
		if e.Thread != root && e.Thread != "" && child {
			text = name + ": " + text
		}
		batch := &d.Errors
		if e.Surface == Progress {
			batch = &d.Progress
		}
		batch.Messages = append(batch.Messages, Message{ID: e.ID, Text: text, Thread: e.Thread, Occurred: e.occurred})
		batch.claims = append(batch.claims, claim{e, e.count})
	}
	if len(d.Errors.Messages)+len(d.Progress.Messages) == 0 {
		return nil
	}
	return d
}

// Waiting reports whether Claim would now deliver a notice on surface, so a
// holder can release an older claim instead of hiding newer feedback.
func (s *Service) Waiting(root string, descendants map[string]string, surface Surface) bool {
	if s == nil || root == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.Surface == surface && e.claimable(root, descendants) {
			return true
		}
	}
	return false
}

func (e *entry) claimable(root string, descendants map[string]string) bool {
	if e.awaitTurn || e.delivered != 0 || e.inFlight {
		return false
	}
	_, child := descendants[e.Thread]
	return e.Thread == root || child || e.Thread == "" && e.Session == ""
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
