package sessionapi

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// BindNativeForeign borrows the invocation's sole native request connection.
// Child endpoints share it; binding neither admits actors nor restores workers.
func (s *Server) BindNativeForeign(receive func(context.Context, appserver.AgentForeignRequest) (appserver.AgentForeignResponse, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nativeForeign = receive
}

// RouteForeign is called only by the source invocation's native host callback.
// A renderer cannot supply authenticatedSource. Native owns the admitted source
// facts, destination validation, source acquisition and durable acceptance.
func (h *Hub) RouteForeign(ctx context.Context, authenticatedSource *Server, request appserver.AgentForeignRequest) (appserver.AgentForeignResponse, error) {
	var empty appserver.AgentForeignResponse
	if !canonicalNativeID(request.Source.RootThreadID) || !canonicalNativeID(request.Source.ActorID) || !canonicalNativeID(request.TargetThreadID) {
		return empty, errors.New("foreign routing requires exact native thread IDs")
	}
	switch request.Operation.Type {
	case "message", "sideQuestion", "list", "inspect":
	default:
		return empty, errors.New("operation is not available across native owners")
	}
	h.mu.Lock()
	sourceRoot := h.sessions[request.Source.RootThreadID]
	sourceActor := h.sessions[request.Source.ActorID]
	destination := h.sessions[request.TargetThreadID]
	active := func(s *listedSession) bool {
		return s != nil && s.server != nil && s.runtime != nil && s.runtime.ctx.Err() == nil && s.context().Err() == nil
	}
	if h.stopping || !active(sourceRoot) || !active(sourceActor) || authenticatedSource == nil ||
		sourceActor.runtime != sourceRoot.runtime ||
		sourceRoot.server != sourceRoot.runtime.server ||
		authenticatedSource != sourceRoot.runtime.server {
		h.mu.Unlock()
		return empty, errors.New("foreign source is not owned by this active native connection")
	}
	if !active(destination) {
		h.mu.Unlock()
		return empty, errors.New("foreign target has no active native owner")
	}
	destinationOwner := destination.runtime
	if destinationOwner.server == nil || destinationOwner.ctx.Err() != nil {
		h.mu.Unlock()
		return empty, errors.New("foreign target's native invocation is unavailable")
	}
	if destinationOwner == sourceRoot.runtime {
		h.mu.Unlock()
		return empty, errors.New("same-tree operations must remain with their native owner")
	}
	owner, lifetime := destinationOwner.server, destination.context()
	h.mu.Unlock()

	owner.mu.Lock()
	receive := owner.nativeForeign
	owner.mu.Unlock()
	if receive == nil {
		return empty, errors.New("foreign target has no borrowed native receiver")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetime, cancel)
	defer stop()
	defer cancel()
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	return receive(ctx, request)
}

func canonicalNativeID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i := range len(value) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if value[i] != '-' {
				return false
			}
			continue
		}
		if !(value[i] >= '0' && value[i] <= '9' || value[i] >= 'a' && value[i] <= 'f') {
			return false
		}
	}
	return true
}
