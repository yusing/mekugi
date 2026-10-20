package executionobservation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	retainedhistory "github.com/yusing/mekugi/modules/sessionhistory/history"
)

func retentionTestSession(t *testing.T, store *mekugiReplayStore, thread string, age time.Duration) (context.Context, func()) {
	t.Helper()
	ctx, release, err := store.beginSession(t.Context(), thread, thread)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if age > 0 {
		retentionTestAge(t, store, thread, age)
	}
	return ctx, release
}

func retentionTestAge(t *testing.T, store *mekugiReplayStore, thread string, age time.Duration) {
	t.Helper()
	if err := store.locked(t.Context(), func(store *mekugiReplayStore) error {
		record, err := store.publication.ReadCatalog(retainedhistory.SessionName(thread))
		if err != nil {
			return err
		}
		record.LastUsed = time.Now().Add(-age)
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return store.transaction.Write(retainedhistory.SessionName(thread), "session-pending-", data)
	}); err != nil {
		t.Fatal(err)
	}
}

func retentionTestPut(t *testing.T, store *mekugiReplayStore, ctx context.Context, workspace, call string) {
	t.Helper()
	if err := store.put(ctx, workspace, map[string]mekugiHistory{call: {ToolName: "shell", Script: "true"}}); err != nil {
		t.Fatal(err)
	}
}

func retentionTestExists(t *testing.T, store *mekugiReplayStore, workspace, call string, want bool) {
	t.Helper()
	_, found, err := store.lookup(t.Context(), workspace, call)
	if err != nil || found != want {
		t.Fatalf("record %s: found=%v want=%v err=%v", call, found, want, err)
	}
}

func (s *mekugiReplayStore) beginSession(ctx context.Context, thread, routing string) (context.Context, func(), error) {
	return beginRetainedSession(ctx, s.publication, s.writes, s.retentionLimits(), thread, routing)
}

func (s *mekugiReplayStore) retainFiles(names ...string) error {
	return requestexecution.StoragePublicationError(s.transaction.Retain(s.session.Thread, s.retentionLimits(), names...))
}

func (s *mekugiReplayStore) maintainStorage(replacement string, size int64) error {
	return requestexecution.StoragePublicationError(s.transaction.Admit(s.retentionLimits(), replacement, size))
}

func (s *mekugiReplayStore) cleanupSessions(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s = s.scoped(ctx)
	return s.publication.Cleanup(ctx, s.writes, s.retentionLimits(), s.session.Thread, func(phase, message string) {
		if s.storageNotice != nil {
			s.storageNotice("", "", phase, message)
		}
	})
}
