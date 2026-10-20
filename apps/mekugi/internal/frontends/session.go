package frontends

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Source: apps/mekugi/internal/router/storage_retention.go:54:78@[7b6d9ccc] beginSession.
// Source: apps/mekugi/internal/router/frontend_recovery.go:24:33@[7b6d9ccc] workerReplayStore.
// The invocation retains its lease through stdout/stderr delivery, without holding
// a publication transaction across command execution or stream writes.
func beginSession(ctx context.Context, service *history.Service, directory, thread string) (scope.Retained, func(), error) {
	selected := scope.Retained{Directory: directory}
	release := func() {}
	if directory == "" {
		return selected, release, nil
	}
	info, err := os.Lstat(filepath.Join(directory, "store.lock"))
	if err != nil || !info.Mode().IsRegular() {
		return selected, nil, errors.New("read recovery storage is missing or invalid")
	}
	if thread == "" {
		return selected, release, nil
	}
	store := service.Attach(directory)
	selected.Thread = thread
	if err := store.Transaction(ctx, nil, func(*history.Transaction) error {
		var err error
		selected.Namespace, err = store.NamespaceForThread(thread)
		return err
	}); err != nil {
		return selected, nil, err
	}
	release, err = store.SessionLease(ctx, thread)
	if err != nil {
		return selected, nil, fmt.Errorf("acquire session storage lease: %w", err)
	}
	if err := store.Transaction(ctx, nil, func(tx *history.Transaction) error {
		return tx.Retain(thread, recovery.DefaultLimits())
	}); err != nil {
		release()
		return selected, nil, err
	}
	return selected, release, nil
}
