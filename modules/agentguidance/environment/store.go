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
	source   *entry
	pending  bool
}

// Store retains only the latest snapshot per admitted thread. No acquisition is
// performed by construction, prewarm, or ordinary repeated inference requests.
type Store struct {
	mu      sync.Mutex
	threads map[string]*entry
}

func (s *Store) Snapshot(ctx context.Context, selected Context, previousRevision uint64) Snapshot {
	s.mu.Lock()
	if s.threads == nil {
		s.threads = make(map[string]*entry)
	}
	current := s.threads[selected.Thread]
	if current != nil && current.pending && current.context.Workspace == selected.Workspace && current.context.Revision == previousRevision {
		// Bind the fork or rewind reload window without re-stamping its
		// inherited facts. An earlier explicit reset invalidates the seed.
		current.context, current.pending = selected, false
	}
	if current == nil || current.context != selected {
		current = &entry{context: selected}
		s.threads[selected.Thread] = current
	}
	s.mu.Unlock()
	return current.load(ctx, selected.Workspace)
}

// Inherit freezes the source entry at confirmed fork admission. Later source
// refreshes cannot change the fork; only a matching admitted workspace can reuse it.
func (s *Store) Inherit(source string, selected Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := s.threads[source]
	if selected.Thread == "" || selected.Thread == source || selected.Workspace == "" || parent == nil ||
		parent.context.Workspace != selected.Workspace || s.threads[selected.Thread] != nil {
		return
	}
	s.threads[selected.Thread] = &entry{context: selected, source: parent, pending: true}
}

// Rewind retains only the admitted workspace's immutable facts across the next
// native reload window. Context revisions still advance for all other consumers.
func (s *Store) Rewind(selected Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.threads[selected.Thread]
	if current == nil || selected.Workspace == "" || current.context.Workspace != selected.Workspace || selected.Revision != current.context.Revision+1 {
		return
	}
	current.context.Revision, current.pending = selected.Revision, true
}

// CancelReuse discards only pending inheritance/rebind state. Ordinary snapshots
// still refresh on the actual native reset, not a possibly failed reset request.
func (s *Store) CancelReuse(thread string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.threads[thread]; current != nil && current.pending {
		delete(s.threads, thread)
	}
}

func (e *entry) load(ctx context.Context, workspace string) Snapshot {
	e.once.Do(func() {
		if e.source != nil {
			e.snapshot = e.source.load(ctx, workspace)
		} else {
			e.snapshot = Acquire(ctx, workspace)
		}
	})
	return e.snapshot
}
