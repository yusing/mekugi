// Package recordtransactions coordinates dependency-first publication across
// retained record owners. Record meaning and encoding remain with each owner.
package recordtransactions

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// PublishEvidence writes the host observations before publishing their change
// membership. The writer borrows this operation's transaction; it cannot start
// another transaction or retain the handle. Updates are returned only on success.
// Source: apps/mekugi/internal/router/mekugi_store.go:149:168@9da47278 put.
func PublishEvidence(ctx context.Context, selected changes.Scope, owner changes.Service, workspace string, publications map[string]changes.Publication, write func(*history.Transaction) error) ([]changes.Update, error) {
	var updates []changes.Update
	err := selected.Store.Transaction(ctx, selected.Writes, func(tx *history.Transaction) error {
		if err := write(tx); err != nil {
			return err
		}
		var err error
		updates, err = owner.Publish(tx, selected, workspace, publications)
		return err
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// Input identifies validated visible facts, not their private record encodings.
// OptionalFiles contribute ownership only when a regular retained file exists.
// Initialize is present only when authenticated ancestry permits scope creation.
// The caller holds a snapshot lease through validation and releases it on return;
// ReleaseSnapshot also releases it before ownership publication under the lock.
type Input struct {
	Workspace       string
	Files           []string
	OptionalFiles   []string
	Changes         []string
	References      []string
	Initialize      *history.Scope
	ReleaseSnapshot func()
}

// RetentionError distinguishes catalog publication failure from invalid evidence.
type RetentionError struct{ Err error }

func (e *RetentionError) Error() string { return e.Err.Error() }
func (e *RetentionError) Unwrap() error { return e.Err }

// RetainInput adopts validated evidence and initializes inherited namespaces in
// one publication view. Fork dependencies precede cloned indexes and scope.
// Completed writes survive failure, so retry uses the same durable identities.
// Source: apps/mekugi/internal/router/storage_retention.go:151:207@9da47278 retainInput;
// apps/mekugi/internal/router/handle_scope.go:45:92@9da47278 initializeHandleScope and cloneHandleData.
func RetainInput(ctx context.Context, selected changes.Scope, owner changes.Service, outputs *recovery.Service, input Input) error {
	release := func() {}
	if input.ReleaseSnapshot != nil {
		release = sync.OnceFunc(input.ReleaseSnapshot)
	}
	return selected.Store.Transaction(ctx, selected.Writes, func(tx *history.Transaction) error {
		files := slices.Clone(input.Files)
		for _, name := range input.OptionalFiles {
			if info, err := os.Lstat(filepath.Join(selected.Store.Directory(), name)); err == nil && info.Mode().IsRegular() {
				files = append(files, name)
			}
		}
		retain := func(names []string) error {
			if err := tx.Retain(selected.Thread, selected.Limits, names...); err != nil {
				return &RetentionError{Err: err}
			}
			return nil
		}
		if identity := input.Initialize; identity != nil {
			if err := tx.InitializeScope(*identity, func(source string) error {
				plan, err := owner.PrepareFork(selected, source, identity.Thread, identity.Fork)
				if err != nil {
					return err
				}
				entries, err := os.ReadDir(selected.Store.Directory())
				if err != nil {
					return err
				}
				inherited := make(map[string]bool)
				for _, name := range files {
					inherited[name] = true
				}
				for _, name := range plan.Dependencies() {
					inherited[name] = true
				}
				for _, entry := range entries {
					if !history.CatalogName(entry.Name()) {
						continue
					}
					catalog, err := selected.Store.ReadCatalog(entry.Name())
					if err != nil {
						return err
					}
					belongs, err := selected.Store.ScopeInNamespace(catalog.Thread, source)
					if err != nil {
						return err
					}
					if belongs {
						maps.Copy(inherited, catalog.Files)
					}
				}
				release()
				if err := retain(slices.Collect(maps.Keys(inherited))); err != nil {
					return err
				}
				return plan.Publish(tx)
			}); err != nil {
				return err
			}
		}
		dependencies, err := owner.Dependencies(selected, input.Workspace, input.Changes, true)
		if err != nil {
			return err
		}
		names := append(files, dependencies...)
		if len(input.References) > 0 {
			session, err := outputs.AttachStore(selected.Store, selected.Namespace, selected.Thread, selected.Limits, selected.Writes)
			if err != nil {
				return err
			}
			for _, id := range input.References {
				dependencies, err := session.Dependencies(id)
				if err != nil {
					return err
				}
				names = append(names, dependencies...)
			}
		}
		release()
		return retain(names)
	})
}
