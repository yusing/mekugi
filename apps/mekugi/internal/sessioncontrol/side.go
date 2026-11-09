// Source: apps/mekugi/internal/router/app_server_btw.go:105:330@[4ded0a4e] side operation correlation and release.
package sessioncontrol

import (
	"fmt"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// ForkClassifier lends request preparation the authoritative auxiliary ledger.
type ForkClassifier interface {
	IsSideThread(string) bool
	IsBTWThread(string) bool
	ObserveCacheKey(thread, key string)
	ForkCacheKey(thread string) string
}

// Side owns live side-thread admission and cleanup independently of Main.
// Closing its presentation does not retire pending fork/start acknowledgements.
type Side struct {
	*conversationmodel.SideAnswer
	Thread, Turn                string
	Waiting                     string // Detached logical-stop identity; the feature owns its lifetime.
	Source, Model, Effort, Tier string
	ForkPending                 bool
	Busy, Starting, Closed      bool
	Interruption                Interruption
	release                     forkRelease
	Settings                    Settings
	LowReasoning                bool
	question                    *AgentQuestion
	cacheKey                    string
}

func (s *Side) PrepareSettings(change map[string]any) SettingsPlan {
	return s.Settings.prepare(preferences.Applied{Model: s.Model, Effort: s.Effort, Tier: s.Tier}, change, s.Turn, s.Thread != "" && !s.Starting && !s.Closed && !s.Unloaded())
}

func (s *Side) Ready(replacing bool) bool {
	return s != nil && !s.Closed && !s.Unloaded() && s.Waiting == "" && !s.Interruption.BeforeStart && s.Interruption.Target == "" && (s.Pending.Text != "" || s.ForkPending) && !s.Starting && s.Turn == "" && !s.Settings.Pending() && !replacing
}

// InterruptTarget keeps a pending fork/start identifiable after host admission.
func (s *Side) InterruptTarget() InterruptTarget {
	if s == nil || s.Closed || s.Unloaded() || !s.Busy && !s.Starting {
		return InterruptTarget{}
	}
	thread := s.Thread
	if thread == "" {
		thread = "side:" + s.Key
	}
	return InterruptTarget{Thread: thread, Turn: s.Turn, Start: fmt.Sprintf("%s:%d", s.Key, s.ID), Wait: s.Waiting}
}

func (s *Side) Close() {
	s.Closed = true
	s.Drafts.Unsent, s.Drafts.Queued = nil, nil
	s.SideAnswer.Close()
}

func (s *Side) Release() string {
	if s.Thread == "" || s.Starting {
		return ""
	}
	return s.release.Next(s.Turn, "thread/archive")
}

func (s *Side) Unloaded() bool { return s.release.Unloaded() }
func (s *Side) Archived() bool { return s.release.Closed() }

func (s *Side) InterruptAcknowledged() {
	s.Turn = ""
	s.release.InterruptAcknowledged()
}

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
	s.Interruption.Finish()
	return true
}

// Side notification routing shares the request ledger's client lifetime. Main
// replacement does not retire it, and a thread close removes its original owner.
func (r *Requests[T]) TrackSide(thread string, owner *Side) {
	r.sidesMu.Lock()
	defer r.sidesMu.Unlock()
	if r.sides == nil {
		r.sides = make(map[string]*Side)
	}
	r.sides[thread] = owner
}
func (r *Requests[T]) Side(thread string) *Side {
	r.sidesMu.RLock()
	defer r.sidesMu.RUnlock()
	return r.sides[thread]
}

// HasSideThread lends admission to the runtime registry without exposing mutable
// side state to inference workers. Cleanup retains the same correlation lifetime.
func (r *Requests[T]) HasSideThread(thread string) bool { return r.Side(thread) != nil }

// IsSideThread identifies answer-only roots, not their admitted descendants.
// An archived fork may still own an open dock and notification correlation.
func (r *Requests[T]) IsSideThread(thread string) bool {
	if r == nil {
		return false
	}
	r.sidesMu.RLock()
	defer r.sidesMu.RUnlock()
	s := r.sides[thread]
	return r.predictions[thread] != nil || s != nil && s.Thread == thread && !s.OneOff && !s.Unloaded()
}

func (r *Requests[T]) IsBTWThread(thread string) bool {
	if r == nil {
		return false
	}
	r.sidesMu.RLock()
	defer r.sidesMu.RUnlock()
	s := r.sides[thread]
	return s != nil && s.Thread == thread && s.LowReasoning && !s.Unloaded()
}

// ObserveCacheKey retains the current source request's effective provider affinity.
// Side requests and predictions cannot replace their source's observed key.
func (r *Requests[T]) ObserveCacheKey(thread, key string) {
	if r == nil || thread == "" {
		return
	}
	r.sidesMu.Lock()
	defer r.sidesMu.Unlock()
	if r.sides[thread] == nil && r.predictions[thread] == nil {
		r.cacheThread, r.cacheKey = thread, key
	}
}

// ForkCacheKey returns only a confirmed side root's frozen source affinity.
// Native descendants keep their own provider routing and thread identities.
func (r *Requests[T]) ForkCacheKey(thread string) string {
	if r == nil {
		return ""
	}
	r.sidesMu.RLock()
	defer r.sidesMu.RUnlock()
	if side := r.sides[thread]; side != nil && side.Thread == thread {
		return side.cacheKey
	}
	return ""
}

func (r *Requests[T]) HasSides() bool {
	if r == nil {
		return false
	}
	r.sidesMu.RLock()
	defer r.sidesMu.RUnlock()
	return len(r.sides) != 0
}
func (r *Requests[T]) CloseSide(thread string) {
	r.sidesMu.Lock()
	defer r.sidesMu.Unlock()
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

// RuntimeThreads borrows only this connection's Main and retained side identities.
// A Side inference does not replace Main's deadline ownership.
func (r *Requests[T]) RuntimeThreads(main string) []string {
	r.sidesMu.RLock()
	defer r.sidesMu.RUnlock()
	threads := []string{main}
	for thread := range r.sides {
		if thread != main {
			threads = append(threads, thread)
		}
	}
	return threads
}

// SideThreads returns identities admitted to this side operation, including host children.
func (r *Requests[T]) SideThreads(owner *Side) []string {
	if r == nil || owner == nil {
		return nil
	}
	r.sidesMu.RLock()
	defer r.sidesMu.RUnlock()
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
