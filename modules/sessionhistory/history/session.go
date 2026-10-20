package history

import (
	"context"

	"github.com/yusing/mekugi/libraries/persistence"
)

// Session is an admitted retained-session selection and its owned lease. Its
// workspace remains an explicit operand of each record operation.
// Source: apps/mekugi/internal/router/app_server_wait_targets.go:17:48@29a3a244.
type Session struct {
	Store             *Store
	Thread, Namespace string
	Limits            Limits
	Writes            *persistence.Counter
	Context           context.Context
	release           func()
}

func (s *Store) SelectSession(ctx context.Context, writes *persistence.Counter, limits Limits, thread string) (Session, error) {
	namespace, release, err := s.BeginSession(ctx, writes, limits, thread)
	if err != nil {
		return Session{}, err
	}
	return Session{Store: s, Thread: thread, Namespace: namespace, Limits: limits, Writes: writes, Context: ctx, release: release}, nil
}

func (s *Session) Close() {
	if s.release != nil {
		s.release()
		s.release = nil
	}
}
