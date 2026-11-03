package sessionapi

import (
	"context"
	"errors"

	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

// BindCoordination borrows the runtime's feature owner and ordinary delivery
// entrypoint. The Hub remains the only authority for active runtime membership.
func (s *Server) BindCoordination(owner *threadcontrol.Service, deliver func(context.Context, string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.coordination, s.deliverCoordination = owner, deliver
}

func (h *Hub) coordinationRuntime(thread string) (*Server, <-chan struct{}, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[thread]
	if s == nil || s.runtime == nil || s.runtime.ctx.Err() != nil {
		return nil, nil, errors.New("coordination target has no active Mekugi runtime")
	}
	return s.server, s.runtime.ctx.Done(), nil
}

// Peer authenticates the source using its registered owner. Exact runtime IDs
// select the destination; task aliases cannot cross session boundaries.
func (h *Hub) Peer(caller threadcontrol.Identity, target string) (threadcontrol.Peer, error) {
	source, _, err := h.coordinationRuntime(caller.Thread)
	if err != nil {
		return threadcontrol.Peer{}, err
	}
	source.mu.Lock()
	owner := source.coordination
	source.mu.Unlock()
	if owner == nil {
		return threadcontrol.Peer{}, errors.New("source has no admitted coordination owner")
	}
	origin, err := owner.Origin(caller)
	if err != nil {
		return threadcontrol.Peer{}, err
	}
	destination, done, err := h.coordinationRuntime(target)
	if err != nil {
		return threadcontrol.Peer{}, err
	}
	destination.mu.Lock()
	peer := threadcontrol.Peer{Source: origin, Control: destination.coordination, Runtime: destination.requests, Done: done, Deliver: destination.deliverCoordination}
	destination.mu.Unlock()
	if peer.Control == nil || peer.Runtime == nil || peer.Deliver == nil {
		return threadcontrol.Peer{}, errors.New("target has no admitted coordination owner")
	}
	peer.Target, err = peer.Control.RemoteTarget(target)
	return peer, err
}
