package router

import (
	"context"
	"errors"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func (s *mekugiReplayStore) prepareHandleScope(ctx context.Context, metadata codexresponses.TurnMetadata) (context.Context, error) {
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
