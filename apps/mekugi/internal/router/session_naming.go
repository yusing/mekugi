package router

import (
	"context"
	"net/http"
	"sync"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

type sessionTitleUpdate struct{ thread, name string }

// sessionNaming owns logical thread scopes and asynchronous result delivery.
// Feature policy lives only in each registered bundle, including across switches.
type sessionNaming struct {
	ctx          context.Context
	provider     inferencegateway.Forwarder
	usage        *accounting.Service
	registration *binding.SessionNaming
	updates      chan sessionTitleUpdate
	mu           sync.Mutex
	threads      map[string]binding.ThreadNaming
}

func newSessionNaming(ctx context.Context, provider inferencegateway.Forwarder, registration *binding.SessionNaming) *sessionNaming {
	if registration == nil {
		return nil
	}
	return &sessionNaming{ctx: ctx, provider: provider, registration: registration, updates: make(chan sessionTitleUpdate, 16), threads: make(map[string]binding.ThreadNaming)}
}

func (g *sessionNaming) register(thread binding.ThreadInfo) {
	if g == nil || thread.ID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if entry := g.threads[thread.ID]; entry != nil {
		entry.ObserveMetadata(thread.Name, len(thread.Turns) != 0)
	} else {
		g.threads[thread.ID] = g.registration.NewThread(thread.Name, len(thread.Turns) != 0)
	}
}

func (g *sessionNaming) scope(thread string) binding.ThreadNaming {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.threads[thread]
}

func (g *sessionNaming) nameChanged(thread, name string) {
	if entry := g.scope(thread); entry != nil {
		entry.NameChanged(name)
	}
}

func (g *sessionNaming) acceptCandidate(thread string) bool {
	entry := g.scope(thread)
	return entry == nil || entry.AcceptCandidate()
}

func (g *sessionNaming) ObserveInput(thread string, input []byte) {
	if entry := g.scope(thread); entry != nil {
		entry.ObserveInput(input)
	}
}

func (g *sessionNaming) Completed(thread string, headers http.Header) {
	entry := g.scope(thread)
	if entry == nil {
		return
	}
	operation := entry.Admit()
	if operation == nil {
		return
	}
	// Admission precedes availability: absent credentials cannot enable a retry
	// on a later exchange. Capture this exchange's auth and originating thread.
	complete := namingCompletion(g.provider, g.usage, thread, headers)
	if complete == nil {
		return
	}
	go func() {
		name, err := operation(g.ctx, complete)
		if err != nil || name == "" {
			return
		}
		select {
		case g.updates <- sessionTitleUpdate{thread: thread, name: name}:
		case <-g.ctx.Done():
		}
	}()
}
