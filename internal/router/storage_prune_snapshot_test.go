package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestUISnapshotBackgroundStorageCleanup(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release := retentionTestSession(t, store, "old", 0)
	retentionTestPut(t, store, ctx, "/w", "old-call")
	release()
	if err := store.locked(t.Context(), func() error {
		catalog, err := store.readRetainedSession(storageSessionName("old"))
		if err != nil {
			return err
		}
		catalog.LastUsed = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		data, err := marshalProtocolJSON(catalog)
		if err != nil {
			return err
		}
		return store.writeFile(storageSessionName("old"), "session-pending-", data)
	}); err != nil {
		t.Fatal(err)
	}
	u, _ := newAppServerTestUI()
	u.issues = NewCriticalErrors()
	u.view.painter.Theme = livediff.DarkTheme
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	u.thread, u.view.conversation = "main", true
	store.storageNotice = func(_, _ string, phase, message string) {
		u.issues.addNotice("", "storage_cleanup_"+phase, message)
		delivery := u.applyCriticalNotices()
		if delivery == nil {
			t.Fatal("cleanup notice did not reach native UI")
		}
		name := "storage-cleanup-reclaimed"
		if strings.Contains(message, "inspected") {
			name = "storage-cleanup-planning"
		}
		assertNativeUISnapshot(t, name, u.view.renderFeed(80, 24).lines)
		delivery.finish(true)
	}
	if err := store.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
}
