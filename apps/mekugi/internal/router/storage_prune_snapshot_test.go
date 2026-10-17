package router

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestUISnapshotBackgroundStorageCleanup(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, release := retentionTestSession(t, store, "old", 0)
	retentionTestPut(t, store, ctx, "/w", "old-call")
	release()
	if err := store.locked(t.Context(), func(store *mekugiReplayStore) error {
		catalog, err := store.publication.ReadCatalog(history.SessionName("old"))
		if err != nil {
			return err
		}
		catalog.LastUsed = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		data, err := responses.MarshalJSON(catalog)
		if err != nil {
			return err
		}
		return store.transaction.Write(history.SessionName("old"), "session-pending-", data)
	}); err != nil {
		t.Fatal(err)
	}
	u, _ := newAppServerTestUI()
	u.issues = testCriticalErrors()
	u.view.Painter.Theme = diffrender.DarkTheme
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	u.Thread, u.view.Conversation = "main", true
	u.testActivity(u.view).testAudience()
	store.storageNotice = func(_, _ string, phase, message string) {
		u.issues.CleanupNotice("", "", phase, message)
		delivery := u.applyCriticalNotices()
		if delivery == nil {
			t.Fatal("cleanup notice did not reach UI")
		}
		name := "storage-cleanup-reclaimed"
		if strings.Contains(message, "inspected") {
			name = "storage-cleanup-planning"
		}
		assertNativeUISnapshot(t, name, u.testActivity(u.view).RenderFeed(80, 24).Lines)
		if err := u.testPaint(io.Discard, 100, 30); err != nil {
			t.Fatal(err)
		}
		delivery.finish(terminalTest(u).Frame.Notices)
	}
	if err := store.cleanupSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
}
