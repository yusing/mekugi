package executionobservation

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func prepareHandleScope(ctx context.Context, store *history.Store, writes *persistence.Counter, metadata codexresponses.TurnMetadata) (context.Context, error) {
	if store == nil {
		return ctx, nil
	}
	identity, _ := ctx.Value(storageSessionKey{}).(storageSessionIdentity)
	err := store.Transaction(ctx, writes, func(*history.Transaction) error {
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
			identity.Namespace, err = store.NamespaceForThread(identity.Parent)
			return err
		}
		// Independent host forks can already be admitted logical children. Their
		// shared namespace is authoritative; host lineage must not rebind it.
		if metadata.SubagentKind == "" && identity.Namespace == identity.Thread {
			identity.Fork = metadata.ForkedFromThreadID
		}
		return nil
	})
	return context.WithValue(ctx, storageSessionKey{}, identity), err
}
