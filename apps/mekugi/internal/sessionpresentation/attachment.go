package sessionpresentation

import (
	"iter"
	"maps"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
)

// Attachment owns feature scopes. Its zero value is ready for use; retirement
// is final. Replacement creates a new Attachment rather than reviving this one.
// Source: apps/mekugi/internal/router/app_server_events.go:37:161@eaad597b;
// apps/mekugi/internal/router/app_server_transcript.go:15:29@eaad597b.
type Attachment struct {
	session    *scope.Store
	threads    map[string]*scope.Store
	turns      map[[2]string]turnScope
	completion scope.Key[completionStreams]
	retired    bool
}

type turnScope struct {
	store *scope.Store
	start string
}

func (a *Attachment) Scope() *scope.Store {
	if a.retired {
		return nil
	}
	if a.session == nil {
		a.session = new(scope.Store)
	}
	return a.session
}

// Start establishes the root's thread scope. Session-scoped feature models may
// already exist before the host returns its root identity.
func (a *Attachment) Start(root string) {
	if a.retired {
		return
	}
	a.threads, a.turns = nil, nil
	a.Thread(root)
}

func (a *Attachment) Thread(id string) *scope.Store {
	if a.retired || id == "" {
		return nil
	}
	if a.threads == nil {
		a.threads = make(map[string]*scope.Store)
	}
	if a.threads[id] == nil {
		a.threads[id] = new(scope.Store)
	}
	return a.threads[id]
}

func (a *Attachment) FindThread(id string) *scope.Store { return a.threads[id] }

func (a *Attachment) Threads() iter.Seq[string] { return maps.Keys(a.threads) }

// Transcript admits an observation without a preceding start. A closed turn's
// nil store remains a tombstone until the attachment is retired.
func (a *Attachment) Transcript(thread, turn string) observation.TextScope {
	if a.retired || thread == "" || turn == "" {
		return observation.TextScope{}
	}
	if a.turns == nil {
		a.turns = make(map[[2]string]turnScope)
	}
	key := [2]string{thread, turn}
	record, known := a.turns[key]
	if !known {
		record.store = new(scope.Store)
		a.turns[key] = record
	}
	return observation.TextScope{Thread: a.Thread(thread), Turn: record.store}
}

func (a *Attachment) CloseTurn(thread, turn string) {
	if a.retired || thread == "" || turn == "" {
		return
	}
	if a.turns == nil {
		a.turns = make(map[[2]string]turnScope)
	}
	key := [2]string{thread, turn}
	record := a.turns[key]
	record.store = nil
	a.turns[key] = record
}

func (a *Attachment) Retire() {
	a.session, a.threads, a.turns = nil, nil, nil
	a.retired = true
}
