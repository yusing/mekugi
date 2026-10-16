package preview

import (
	"context"

	"github.com/yusing/mekugi/contracts/scope"
)

// Service resolves module state from the application's attached-session store.
// Construction retains no preview, subscription, context or thread state.
type Service struct{ session scope.Key[Session] }

func (s *Service) Attach(ctx context.Context, store *scope.Store) *Session {
	if s == nil {
		return nil
	}
	session := s.session.Value(store)
	if session.ctx == nil {
		session.ctx, session.cancel = context.WithCancel(ctx)
		session.scope = Scope{Workspaces: make(map[string]map[string]bool)}
	}
	return session
}
