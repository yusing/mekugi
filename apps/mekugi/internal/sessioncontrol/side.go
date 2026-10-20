// Source: apps/mekugi/internal/router/app_server_btw.go:105:330@[4ded0a4e] side operation correlation and release.
package sessioncontrol

import conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

// Side owns live side-thread admission and cleanup independently of Main.
// Closing its presentation does not retire pending fork/start acknowledgements.
type Side struct {
	*conversationmodel.SideAnswer
	Thread, Turn                string
	Source, Model, Effort, Tier string
	ForkPending, FromSide       bool
	Busy, Starting, Closed      bool
	Interrupting, Unloading     bool
	RestoreTarget               *conversationmodel.Session
}

func (s *Side) Ready(replacing bool) bool {
	return s != nil && (s.Pending.Text != "" || s.ForkPending) && !s.Starting && s.Turn == "" && !replacing
}

func (s *Side) Close() {
	s.Closed = true
	s.SideAnswer.Close()
}

func (s *Side) Release() string {
	if s.Thread == "" || s.Starting || s.Interrupting || s.Unloading {
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
func (r *Requests[T]) CloseSide(thread string)  { delete(r.sides, thread) }
