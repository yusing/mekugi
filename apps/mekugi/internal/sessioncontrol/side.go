// Source: apps/mekugi/internal/router/app_server_btw.go:105:330@[4ded0a4e] side operation correlation and release.
package sessioncontrol

import (
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// Side owns live side-thread admission and cleanup independently of Main.
// Closing its presentation does not retire pending fork/start acknowledgements.
type Side struct {
	*conversationmodel.SideAnswer
	Thread, Turn                string
	Source, Model, Effort, Tier string
	ForkPending                 bool
	Busy, Starting, Closed      bool
	Interrupting, Unloading     bool
	Unloaded                    bool
	Settings                    Settings
	LowReasoning                bool
}

func (s *Side) PrepareSettings(change map[string]any) SettingsPlan {
	return s.Settings.prepare(preferences.Applied{Model: s.Model, Effort: s.Effort, Tier: s.Tier}, change, s.Turn, s.Thread != "" && !s.Starting && !s.Closed && !s.Unloaded)
}

func (s *Side) Ready(replacing bool) bool {
	return s != nil && !s.Closed && !s.Unloaded && (s.Pending.Text != "" || s.ForkPending) && !s.Starting && s.Turn == "" && !s.Settings.Pending() && !replacing
}

func (s *Side) Close() {
	s.Closed = true
	s.Drafts.Unsent, s.Drafts.Queued = nil, nil
	s.SideAnswer.Close()
}

func (s *Side) Release() string {
	if s.Thread == "" || s.Starting || s.Interrupting || s.Unloading || s.Unloaded {
		return ""
	}
	if s.Turn != "" {
		s.Interrupting = true
		return "turn/interrupt"
	}
	s.Unloading = true
	return "thread/unsubscribe"
}

func (s *Side) InterruptAcknowledged() { s.Turn, s.Interrupting = "", false }

func (s *Side) StartAcknowledged(turn string) {
	s.Starting = false
	if s.Busy {
		s.Turn = turn
	}
}

func (s *Side) Complete(turn string) bool {
	if s.Turn != "" && s.Turn != turn {
		return false
	}
	s.Turn, s.Busy = "", false
	return true
}

// Side notification routing shares the request ledger's client lifetime. Main
// replacement does not retire it, and a thread close removes its original owner.
func (r *Requests[T]) TrackSide(thread string, owner *Side) {
	if r.sides == nil {
		r.sides = make(map[string]*Side)
	}
	r.sides[thread] = owner
}
func (r *Requests[T]) Side(thread string) *Side { return r.sides[thread] }
func (r *Requests[T]) HasSides() bool           { return r != nil && len(r.sides) != 0 }
func (r *Requests[T]) CloseSide(thread string) {
	owner := r.sides[thread]
	if owner != nil && owner.Closed && owner.Thread == thread {
		for id, side := range r.sides {
			if side == owner {
				delete(r.sides, id)
			}
		}
	} else {
		delete(r.sides, thread)
	}
}

func (r *Requests[T]) WorkSide(thread string) bool {
	s := r.Side(thread)
	return s != nil && s.OneOff && !s.Closed
}

// SideThreads returns identities admitted to this side operation, including host children.
func (r *Requests[T]) SideThreads(owner *Side) []string {
	if r == nil || owner == nil {
		return nil
	}
	var threads []string
	for thread, side := range r.sides {
		if side == owner {
			threads = append(threads, thread)
		}
	}
	return threads
}

// AdmitSideChild shares notification and metadata ancestry admission.
func (r *Requests[T]) AdmitSideChild(thread, parent string) {
	if side := r.Side(parent); thread != "" && thread != parent && side != nil && side.OneOff {
		r.TrackSide(thread, side)
	}
}

// RestoreInput keeps a rejected descendant companion scoped to its question.
// Sync answers have already been committed, so retry only the companion payload.
func (s *Side) RestoreInput(input conversationmodel.Input) (bool, conversationmodel.InputRestoration) {
	if input.Question != nil && input.Question.Sync && input.Question.Thread != s.Thread {
		s.Drafts.Queued = append(s.Drafts.Queued, input)
		return true, conversationmodel.InputRestoration{}
	}
	return false, s.Drafts.RestoreInput(input)
}
