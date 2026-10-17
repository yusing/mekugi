package router

import (
	"context"
	"fmt"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Retain and admit prepared native metadata before publishing its bytes.
// Source: apps/mekugi/internal/router/storage_retention.go:217:226@[55257c48] writeManagedFile.
func writeRetainedRecord(tx *history.Transaction, thread string, limits history.Limits, name, pattern string, data []byte) error {
	if err := requestexecution.StoragePublicationError(tx.Retain(thread, limits, name)); err != nil {
		return requestexecution.StorageIOError(err)
	}
	if err := requestexecution.StoragePublicationError(tx.Admit(limits, name, int64(len(data)))); err != nil {
		return err
	}
	return requestexecution.StorageIOError(tx.Write(name, pattern, data))
}

// Age-based cleanup is router maintenance, not part of any request's replay
// view. An unrelated catalog must not prevent a new thread from starting.
type retentionScope struct {
	store  *history.Store
	writes *persistence.Counter
	limits history.Limits
	thread string
	notice func(string, string)
}

func runStorageRetention(ctx context.Context, diagnostics *diagnostic.Service, store func() retentionScope, notice func(error)) {
	var debugSweep, sessionSweep time.Time
	cleanup := func() {
		if diagnostics != nil && time.Since(debugSweep) >= time.Hour {
			debugSweep = time.Now()
			root, err := debugStorageDirectory()
			if err == nil {
				err = diagnostics.CleanupBundles(ctx, root, debugSweep)
			}
			if err != nil && ctx.Err() == nil && notice != nil {
				notice(fmt.Errorf("debug bundle cleanup: %w", err))
			}
		}
		s := store()
		if s.store == nil {
			return
		}
		var err error
		if time.Since(sessionSweep) < time.Minute {
			// Bound full age replanning when publications invalidate ownership.
			// Pressure still gets the worker's one-second response interval.
			var pressure bool
			err = s.store.Transaction(ctx, s.writes, func(tx *history.Transaction) error {
				var err error
				pressure, err = tx.PendingCleanup()
				return err
			})
			if err == nil && !pressure {
				return
			}
		}
		if err == nil {
			sessionSweep = time.Now()
			err = s.store.Cleanup(ctx, s.writes, s.limits, s.thread, s.notice)
		}
		if err != nil && ctx.Err() == nil && notice != nil {
			notice(fmt.Errorf("session cleanup: %w", err))
		}
	}
	cleanup()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}
