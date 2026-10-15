package history

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/persistence"
)

// Service owns the composed retained-record catalog for an invocation.
// Store handles share process-independent coordination at their retained path.
type Service struct{ kinds []recordkind.Spec }

func NewService(kinds []recordkind.Spec) (*Service, error) {
	seen := make(map[string]bool)
	for _, kind := range kinds {
		if kind.ID == "" || kind.Match == nil {
			return nil, errors.New("record kind requires identity and recognition")
		}
		if seen[kind.ID] {
			return nil, fmt.Errorf("duplicate record kind %q", kind.ID)
		}
		seen[kind.ID] = true
	}
	return &Service{kinds: slices.Clone(kinds)}, nil
}

func (s *Service) Kind(name string) (recordkind.Spec, bool, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return recordkind.Spec{}, false, errors.New("invalid managed record identity")
	}
	var result recordkind.Spec
	found := false
	for _, kind := range s.kinds {
		if !kind.Match(name) {
			continue
		}
		if found {
			return recordkind.Spec{}, false, fmt.Errorf("ambiguous record kind for %q", name)
		}
		result, found = kind, true
	}
	return result, found, nil
}

// Attach is read-side construction and never creates a directory or lock file.
func (s *Service) Attach(directory string) *Store {
	return &Store{directory: directory, service: s}
}

func (s *Service) Open(ctx context.Context, directory string) (*Store, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if err := persistence.EnsurePrivateDirectory(directory); err != nil {
		return nil, err
	}
	store := s.Attach(directory)
	if err := store.Transaction(ctx, nil, func(*Transaction) error { return nil }); err != nil {
		return nil, err
	}
	return store, nil
}
