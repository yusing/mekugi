package feed

import "github.com/yusing/mekugi/contracts/scope"

// Service accesses activity state owned by the application's attachment store.
// It holds no per-session map, host resources or background work.
type Service struct {
	outputs scope.Key[Retention]
}

// Outputs returns the attachment's shared retention, or nil when activity is absent.
func (s *Service) Outputs(store *scope.Store) *Retention {
	if s == nil {
		return nil
	}
	return s.outputs.Value(store)
}
