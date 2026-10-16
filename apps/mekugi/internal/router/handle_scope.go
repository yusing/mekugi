package router

import (
	"context"
	"errors"
	"maps"
	"os"
	"slices"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func (s *mekugiReplayStore) prepareHandleScope(ctx context.Context, metadata codexTurnMetadata) (context.Context, error) {
	if s == nil {
		return ctx, nil
	}
	identity, _ := ctx.Value(storageSessionKey{}).(storageSessionIdentity)
	err := s.locked(ctx, func(s *mekugiReplayStore) error {
		if metadata.ThreadID != "" && metadata.ThreadID != identity.Thread ||
			metadata.SubagentKind != "" && metadata.ParentThreadID == "" {
			return nil // Invalid auxiliary ancestry cannot change a retained binding.
		}
		// Agent names control presentation, not session identity. A malformed
		// name must not discard an otherwise valid stable parent relationship.
		identity.Initialize = true
		if metadata.SubagentKind != "" && metadata.ParentThreadID != "" {
			identity.Parent = metadata.ParentThreadID
			if identity.Parent == identity.Thread {
				return errors.New("handle scope parent cannot refer to itself")
			}
			var err error
			identity.Namespace, err = s.publication.NamespaceForThread(identity.Parent)
			return err
		}
		if metadata.SubagentKind == "" {
			identity.Fork = metadata.ForkedFromThreadID
		}
		return nil
	})
	return context.WithValue(ctx, storageSessionKey{}, identity), err
}

// Invoked only after replay validation, before retaining/confirming inherited
// references. Publishing the binding last makes an interrupted clone retryable.
func (s *mekugiReplayStore) initializeHandleScope(visibleFiles []string, releaseSnapshot func()) error {
	identity := s.session
	if !identity.Initialize {
		return nil
	}
	return s.transaction.InitializeScope(history.Scope{Thread: identity.Thread, Namespace: identity.Namespace, Parent: identity.Parent, Fork: identity.Fork}, func(source string) error {
		return s.cloneHandleData(source, identity.Thread, identity.Fork, visibleFiles, releaseSnapshot)
	})
}

func (s *mekugiReplayStore) cloneHandleData(source, dest, fork string, visibleFiles []string, releaseSnapshot func()) error {
	plan, err := s.changes.PrepareFork(s.changeScope(), source, dest, fork)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return err
	}
	files := make(map[string]bool)
	for _, name := range append(visibleFiles, plan.Dependencies()...) {
		files[name] = true
	}
	for _, entry := range entries {
		if !history.CatalogName(entry.Name()) {
			continue
		}
		catalog, err := s.publication.ReadCatalog(entry.Name())
		if err != nil {
			return err
		}
		scope, _, err := s.publication.ReadScope(catalog.Thread)
		if err != nil {
			return err
		}
		if scope.Namespace == source {
			maps.Copy(files, catalog.Files)
		}
	}
	// The caller's transaction bridges validated dependencies to retained ownership.
	if releaseSnapshot != nil {
		releaseSnapshot()
	}
	if err := s.retainFiles(slices.Collect(maps.Keys(files))...); err != nil {
		return err
	}
	return changeError(plan.Publish(s.transaction))
}
