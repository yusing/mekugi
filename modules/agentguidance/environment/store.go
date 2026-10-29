package environment

import (
	"context"
	"sync"
)

// Context identifies authoritative runtime context, not an inference request.
type Context struct {
	Session, Thread, Workspace string
	Revision                   uint64
}

type entry struct {
	context  Context
	once     sync.Once
	snapshot Snapshot
}

// Store retains only the latest snapshot per admitted thread. No acquisition is
// performed by construction, prewarm, or ordinary repeated inference requests.
type Store struct {
	mu      sync.Mutex
	threads map[string]*entry
}

func (s *Store) Snapshot(ctx context.Context, selected Context) Snapshot {
	s.mu.Lock()
	if s.threads == nil {
		s.threads = make(map[string]*entry)
	}
	current := s.threads[selected.Thread]
	if current == nil || current.context != selected {
		current = &entry{context: selected}
		s.threads[selected.Thread] = current
	}
	s.mu.Unlock()
	current.once.Do(func() { current.snapshot = Acquire(ctx, selected.Workspace) })
	return current.snapshot
}
