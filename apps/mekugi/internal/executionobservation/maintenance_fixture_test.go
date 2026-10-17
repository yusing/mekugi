package executionobservation

import (
	"context"
	"testing"
	"time"
)

func startResponseRetentionWorker(t *testing.T, store *mekugiReplayStore) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if err := store.cleanupSessions(ctx); err != nil && ctx.Err() == nil {
				t.Errorf("cleanup: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}
