package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/ui/diffview"
	"github.com/yusing/mekugi/libraries/diffrender"
)

func TestNativePreviewMailboxKeepsNewestFile(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	attach := func() *appServerUI {
		u := nativeBatchFixture(t)
		u.shell.diff.store = store
		return u
	}
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{"/workspace": {"thread": true}}})
	sub := broker.subscribe()
	older := nativeBatchPreview("older", "/root", "older")
	newer := nativeBatchPreview("newer", "/root", "newer")
	older.Thread, newer.Thread = "thread", "thread"
	broker.publishPreview(older, false)
	broker.publishPreview(newer, false)
	// Replacing an older call's queued snapshot must not make its file newest.
	broker.publishPreview(older, false)
	drain := func(u *appServerUI) {
		for _, event := range broker.takePreviews(sub) {
			u.shell.applyDiff(t.Context(), event)
		}
		if u.shell.diffFailure != "" {
			t.Fatal(u.shell.diffFailure)
		}
		revealNativeDock(u.shell)
	}
	requireNewest := func(u *appServerUI) {
		rows, err := u.shell.liveDock.RenderBatch(t.Context(), "/root", "/workspace", diffrender.DarkTheme, 100, 16, 15)
		if err != nil {
			t.Fatal(err)
		}
		if frame := strings.Join(rows, "\n"); !strings.Contains(frame, "newer_line_40") {
			t.Fatalf("latest source window lost the newest file: %s", frame)
		}
	}
	u := attach()
	drain(u)
	requireNewest(u)
	older.Complete, older.Evaluated = true, true
	broker.publishPreview(older, false)
	drain(u)
	u.shell.liveDock.ExpireBatches(time.Now().Add(2*time.Second), nativeDockMinimum)
	if u.shell.liveDock.Views[older.ID] != nil {
		t.Fatal("completed source did not retire")
	}
	sub = broker.subscribe()
	drain(u)
	if u.shell.liveDock.Views[older.ID] != nil {
		t.Fatal("resynchronization reopened an expired completion")
	}
	for i := range 33 {
		broker.publishPreview(diffview.Preview{ID: fmt.Sprintf("absent-%d", i)}, true)
	}
	select {
	case <-sub.gap:
	default:
		t.Fatal("preview overflow did not request resynchronization")
	}
	sub = broker.subscribe()
	// A new attachment must get the same order from the retained broker snapshot.
	restored := attach()
	drain(restored)
	requireNewest(restored)
	broker.resync(liveDiffScope{Workspaces: map[string]map[string]bool{}})
	sub = broker.subscribe()
	drain(restored)
	if len(restored.shell.liveDock.Order) != 0 || len(restored.shell.livePending) != 0 {
		t.Fatal("resynchronization retained previews outside its scope")
	}
}
