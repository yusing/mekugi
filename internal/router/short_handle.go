package router

import (
	"context"
	"errors"
)

// Callers already holding store.lock use the locked variant.
func (s *mekugiReplayStore) allocateHandles(ctx context.Context, count int) ([]string, error) {
	if s == nil {
		return nil, errors.New("handle allocation storage is unavailable")
	}
	s = s.scoped(ctx)
	var handles []string
	err := s.locked(ctx, func(s *mekugiReplayStore) error {
		var err error
		handles, err = s.transaction.ReserveHandles(s.handleNamespace(), count)
		return err
	})
	return handles, err
}
