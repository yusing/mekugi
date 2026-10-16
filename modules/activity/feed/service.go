package feed

import "github.com/yusing/mekugi/contracts/scope"

// Service accesses activity state owned by the application's attachment store.
// It holds no per-session map, host resources or background work.
type Service struct {
	outputs scope.Key[Retention]
	model   scope.Key[any]
}

// Outputs returns the attachment's shared retention, or nil when activity is absent.
func (s *Service) Outputs(store *scope.Store) *Retention {
	if s == nil {
		return nil
	}
	return s.outputs.Value(store)
}

// Model returns the binding's typed feed state for this attachment. A binding uses
// one model type throughout an attachment. The store retires it with the outputs.
func (s *Service) Model[T any](store *scope.Store) *T {
	if s == nil {
		return nil
	}
	slot := s.model.Value(store)
	if *slot == nil {
		*slot = new(T)
	}
	return (*slot).(*T)
}
