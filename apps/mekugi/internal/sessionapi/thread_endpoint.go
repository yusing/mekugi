package sessionapi

import (
	"context"
	"errors"

	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

// AdoptThread registers a native-confirmed thread beneath the actual invocation.
// It creates a presentation/control endpoint, not execution or a logical member.
// The caller is the native connection owner, never a renderer request.
func (h *Hub) AdoptThread(owner *Server, thread, parent, workspace string, end func(context.Context) error) (*Server, error) {
	if thread == "" || parent == "" || thread == parent || end == nil {
		return nil, errors.New("native endpoint requires confirmed child identity and an end owner")
	}
	owner.mu.Lock()
	profile := owner.profileIdentity
	owner.mu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	var runtime *managedSession
	for candidate := range h.running {
		if candidate.server == owner {
			runtime = candidate
			break
		}
	}
	if runtime == nil || runtime.ctx.Err() != nil || h.stopping {
		return nil, errors.New("native invocation is unavailable")
	}
	if h.sessions[thread] != nil {
		return nil, errThreadOwned
	}
	for candidate := range h.running {
		if candidate.resume == thread {
			return nil, errThreadOwned
		}
	}
	server := NewPreparedLive(owner.prepare)
	server.preparations, server.activeThread = owner.preparations, h.activeThread
	server.profileIdentity = profile
	server.questionOwner = func(call string) *Server { return h.questionOwner(server, call) }
	server.family = func() (*Family, *changes.ReviewView) { return h.Family(thread) }
	server.reserveThread = func(target string) error {
		if target != "" && target != thread {
			return errors.New("an admitted native child cannot replace its identity")
		}
		return nil
	}
	server.registerSummary = func(summary Summary) error {
		if summary.Thread == "" {
			return nil
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		current := h.sessions[thread]
		if h.stopping || runtime.ctx.Err() != nil || current == nil || current.server != server {
			return context.Canceled
		}
		if summary.Thread != thread {
			return errors.New("native endpoint changed thread identity")
		}
		entry := projectSummary(summary)
		entry.ID, entry.ParentThread = thread, parent
		current.entry = entry
		h.publishList()
		return nil
	}
	endpointContext, release := context.WithCancel(runtime.ctx)
	h.sessions[thread] = &listedSession{entry: listed{ID: thread, Thread: thread, ParentThread: parent, Workspace: workspace}, server: server, runtime: runtime, end: end, endpointContext: endpointContext, releaseEndpoint: release}
	return server, nil
}

// endpointsLocked enumerates borrowed publications, including a root that has
// not yet published its identity. It is not an execution or membership registry.
func (h *Hub) endpointsLocked() map[*Server]*managedSession {
	endpoints := make(map[*Server]*managedSession, len(h.sessions)+len(h.running))
	for _, endpoint := range h.sessions {
		endpoints[endpoint.server] = endpoint.runtime
	}
	for runtime := range h.running {
		endpoints[runtime.server] = runtime
	}
	return endpoints
}

// RetireThread follows confirmed native end/join or failed endpoint acquisition.
// It releases no invocation, sibling, saved history or native subscription.
func (h *Hub) RetireThread(server *Server, thread string, outcome error) {
	h.mu.Lock()
	current := h.sessions[thread]
	if current == nil || current.server != server || current.runtime == nil || current.runtime.server == server {
		h.mu.Unlock()
		return
	}
	delete(h.sessions, thread)
	h.publishList()
	h.mu.Unlock()
	current.releaseEndpoint()
	server.Native().Finish(outcome)
}
