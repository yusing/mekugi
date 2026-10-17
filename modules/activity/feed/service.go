package feed

import (
	"fmt"
	"slices"

	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity/transcript"
)

// Service accesses activity state owned by the application's attachment store.
// It holds no per-session map, host resources or background work.
type Service struct {
	details    []activitydetail.Provider
	outputs    scope.Key[Retention]
	model      scope.Key[any]
	transcript scope.Key[transcript.State]
}

func New(details []activitydetail.Provider) (*Service, error) {
	seen := make(map[string]bool)
	for _, provider := range details {
		if provider.Name == "" || provider.Resolve == nil {
			return nil, fmt.Errorf("Activity detail provider requires a name and resolver")
		}
		if seen[provider.Name] {
			return nil, fmt.Errorf("duplicate Activity detail provider %q", provider.Name)
		}
		seen[provider.Name] = true
	}
	return &Service{details: slices.Clone(details)}, nil
}

func (s *Service) ActivityDetail(store *scope.Store, request activitydetail.Request) activitydetail.Result {
	if s != nil {
		for _, provider := range s.details {
			if result := provider.Resolve(store, request); len(result.Pages) > 0 {
				return result
			}
		}
	}
	return activitydetail.Result{}
}

func (s *Service) Transcript(store *scope.Store) *transcript.State { return s.transcript.Value(store) }

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
