package sessionapi

import (
	"context"
	"errors"

	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

func (s *Server) BindRequests(requests threadcontrol.Runtime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = requests
}

// BindSideThreads borrows the request owner's concurrent-safe side admission.
// Side forks use their existing host connection, not an independent runtime.
func (s *Server) BindSideThreads(owns func(string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sideThreads = owns
}

func (h *Hub) Requests(thread string) (threadcontrol.Runtime, error) {
	h.mu.Lock()
	s := h.sessions[thread]
	if s == nil || s.runtime == nil || s.runtime.ctx.Err() != nil {
		runtimes := make([]*managedSession, 0, len(h.running))
		for runtime := range h.running {
			runtimes = append(runtimes, runtime)
		}
		h.mu.Unlock()
		// Publication can enter Hub while holding the server lock. Resolve the
		// borrowed request owner only after releasing the registry lock.
		for _, runtime := range runtimes {
			if runtime.ctx.Err() != nil {
				continue
			}
			server := runtime.server
			server.mu.Lock()
			owns, requests := server.sideThreads, server.requests
			server.mu.Unlock()
			if requests != nil && owns != nil && owns(thread) {
				return requests, nil
			}
		}
		return nil, errors.New("thread has no running native runtime")
	}
	server := s.server
	h.mu.Unlock()
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.requests == nil {
		return nil, errors.New("native runtime has no admitted request owner")
	}
	return server.requests, nil
}

// StartPrepared admits execution owned by an existing native runtime. Launch
// supplies frozen execution dependencies; Hub retains the single runtime registry
// and joins child lifetimes. No renderer or ordinary initial input is required.
var ErrPreparedNotStarted = errors.New("prepared runtime was not started")

func (h *Hub) StartPrepared(request context.Context, parent, resume, workspace string, run func(context.Context, *Server) error) (string, error) {
	if parent == "" || run == nil {
		return "", errors.Join(ErrPreparedNotStarted, errors.New("prepared runtime requires a parent and execution owner"))
	}
	m, _, err := h.admitRuntime(request, nil, Start{Workspace: workspace, Resume: resume}, parent, run)
	if err != nil {
		return "", errors.Join(ErrPreparedNotStarted, err)
	}
	return m.waitReady(request)
}

// BindChildAttachment supplies the prepared runtime owner used by explicit
// view navigation. It never supplies or dispatches a work assignment.
func (s *Server) BindChildAttachment(attach func(context.Context, string, string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.childAttachment = attach
}

func (h *Hub) attachThread(ctx context.Context, thread string, authority viewAuthority) *RPCError {
	if failure := h.attachmentFailure(thread); failure != nil {
		return failure
	}
	if _, failure := h.lookup(thread, "", authority); failure == nil {
		return nil
	}
	// Resolve only logical ancestors in admitted membership. Physical lifetime
	// ancestry survives thread replacement and cannot authorize preparation.
	h.mu.Lock()
	var owner *listedSession
	var ownerFamily string
	for _, source := range h.sessions {
		if !source.server.live || source.runtime == nil || source.runtime.ctx.Err() != nil {
			continue
		}
		frame := source.server.frame.Load()
		if frame == nil || frame.FamilyRoot == "" || frame.Session.Thread != source.entry.Thread {
			continue
		}
		parents := make(map[string]string, len(frame.Members))
		for _, member := range frame.Members {
			parents[member.Thread] = member.Parent
		}
		var candidate *listedSession
		for parent := parents[thread]; parent != ""; parent = parents[parent] {
			if ancestor := h.sessions[parent]; ancestor != nil && ancestor.server.live && ancestor.runtime != nil && ancestor.runtime.ctx.Err() == nil {
				if current := ancestor.server.frame.Load(); current != nil && current.Session.Thread == parent && current.FamilyRoot == frame.FamilyRoot {
					// Prefer the highest live ancestor so peers share its lock.
					candidate = ancestor
				}
			}
			if parent == frame.FamilyRoot {
				owner, ownerFamily = candidate, frame.FamilyRoot
				break
			}
		}
		if owner != nil {
			break
		}
	}
	h.mu.Unlock()
	if owner == nil {
		return rpcFailure(404, "The selected thread has no admitted resumable runtime.")
	}
	s := owner.server
	s.childAttach.Lock()
	defer s.childAttach.Unlock()
	if _, failure := h.lookup(thread, "", authority); failure == nil {
		return nil
	}
	selected, failure := h.lookup(owner.entry.Thread, "", authority)
	if failure != nil {
		return failure
	}
	frame := s.frame.Load()
	if selected.server != s || selected.runtime != owner.runtime || owner.runtime.ctx.Err() != nil || frame == nil || frame.Session.Thread != owner.entry.Thread || frame.FamilyRoot != ownerFamily {
		return rpcFailure(409, "The preparation owner is no longer available.")
	}
	s.mu.Lock()
	attach := s.childAttachment
	s.mu.Unlock()
	if attach == nil {
		return rpcFailure(404, "The selected thread has no resumable runtime owner.")
	}
	if err := attach(ctx, owner.entry.Thread, thread); err != nil {
		if errors.Is(err, errRuntimeUnknown) {
			return runtimeStartFailure(err)
		}
		return rpcFailure(409, err.Error())
	}
	return nil
}

// Explicit roster capability precedes runtime lookup, including already-running
// delegates. Runtime registration alone does not admit an interactive view.
func (h *Hub) attachmentFailure(thread string) *RPCError {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, source := range h.sessions {
		frame := source.server.frame.Load()
		if frame == nil || frame.Session.Thread != source.entry.Thread {
			continue
		}
		for _, member := range frame.Members {
			if member.Thread == thread && member.NonAttachable {
				return rpcFailure(403, "The selected agent does not support an interactive view.")
			}
		}
		if frame.Agents != nil {
			for _, row := range frame.Agents.Roster {
				if row.Agent.Thread == thread && row.Agent.NonAttachable {
					return rpcFailure(403, "The selected agent does not support an interactive view.")
				}
			}
		}
	}
	return nil
}
