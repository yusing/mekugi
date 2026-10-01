package router

import (
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

func resetWordingCompaction(t *testing.T, d *journalResetDriver, fallback bool) {
	t.Helper()
	if fallback {
		if err := d.change(func(j *threadJournal, _ *journalResetIntent) error {
			j.IdentityConflicted = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	request, headers := journalCompactionRequest(t, d.workspace, d.thread)
	metadata, _ := decodeCodexTurnMetadata(headers)
	metadata.Compaction = mustTestJSON(t, map[string]any{
		"trigger": "manual", "reason": "user_requested", "implementation": "responses",
		"phase": "standalone_turn", "strategy": "memento",
	})
	headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
	wire, err := journalCompactionSSE("provider-compact", "gpt-test", "Provider recovery")
	if err != nil {
		t.Fatal(err)
	}
	response := serverHTTPResponse(string(wire))
	response.Header.Set("Content-Type", "text/event-stream")
	provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
	if err := executeRequest(t.Context(), t.Context(), request, headers, "compact", provider, io.Discard, nil, d.proxy); err != nil {
		t.Fatal(err)
	}
	if (len(provider.forwarded) > 0) != fallback {
		t.Fatalf("provider forwarding: %d, fallback=%v", len(provider.forwarded), fallback)
	}
}

func TestUISnapshotNativeResetWording(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for _, mode := range []string{"slice", "auto", "off"} {
		t.Run(mode, func(t *testing.T) {
			d, _ := resetDriverFixture(t, mode)
			u := newAppServerSessionTestUI(t, d.workspace)
			u.thread, u.reset = d.thread, d
			u.session.start(d.thread, d.workspace)
			u.view.painter.Theme = livediff.DarkTheme
			// Render the driver's real countdown without wall-clock-dependent timing.
			d.deadline = now.Add(3 * time.Second)
			assertNativeUISnapshot(t, "native-reset-countdown-"+mode, []string{d.label(now)})
			if mode == "off" {
				return
			}
			if err := d.tick(d.deadline); err != nil {
				t.Fatal(err)
			}
			assertNativeUISnapshot(t, "native-reset-dispatched-"+mode, []string{d.label(now)})
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": d.thread, "turn": map[string]any{"id": "reset-turn"}})
			item := map[string]any{"id": "reset-item", "type": "contextCompaction"}
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": d.thread, "turnId": "reset-turn", "item": item})
			u.turnStarted = now
			assertNativeUISnapshot(t, "native-reset-working-"+mode, []string{u.sessionLabel(now)})
			resetWordingCompaction(t, d, false)
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": d.thread, "turnId": "reset-turn", "item": item})
			// Keep ordinary provider compaction wording even in reset-enabled sessions.
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": d.thread, "turnId": "provider-turn", "item": map[string]any{"id": "provider-item", "type": "contextCompaction"}})
			u.turn = "" // No duration-dependent status in the completion frame.
			rows, _ := u.mainFrame(100, 16, 0)
			assertNativeUISnapshot(t, "native-reset-completed-"+mode, rows)
		})
	}
}

func TestUISnapshotNativeResetRestored(t *testing.T) {
	d, _ := resetDriverFixture(t, "slice")
	if err := d.tick(d.deadline); err != nil {
		t.Fatal(err)
	}
	resetDriverEvent(t, d, "turn/started", fmt.Sprintf(`{"threadId":%q,"turn":{"id":"reset-turn"}}`, d.thread))
	resetWordingCompaction(t, d, false)
	if err := d.continuePlan(true); err != nil {
		t.Fatal(err)
	}
	// Use a fresh store/view, not the live driver's identity.
	reopened, err := openMekugiReplayStore(d.proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, d.workspace)
	u.thread = d.thread
	u.session.start(d.thread, d.workspace)
	store := newJournalStore()
	u.journal = store.attachNative(d.workspace, d.thread)
	if err := store.restoreNative(t.Context(), reopened, u.journal); err != nil {
		t.Fatal(err)
	}
	u.view.painter.Theme = livediff.DarkTheme
	u.restoreHistory([]appServerHistoryTurn{
		{ID: "reset-turn", Status: "completed", Items: []appServerItem{{ID: "reset", Type: "contextCompaction"}}},
		{ID: "provider-turn", Status: "completed", Items: []appServerItem{{ID: "provider", Type: "contextCompaction"}}},
	})
	rows, _ := u.mainFrame(100, 16, 0)
	assertNativeUISnapshot(t, "native-reset-restored", rows)
	if u.journalResetTurn("child", "reset-turn") || u.journalResetTurn(d.thread, "") {
		t.Fatal("reset identity leaked to another thread or unknown turn")
	}
}

func TestUISnapshotNativeResetProviderFallback(t *testing.T) {
	for _, mode := range []string{"slice", "auto"} {
		t.Run(mode, func(t *testing.T) {
			d, _ := resetDriverFixture(t, mode)
			if err := d.tick(d.deadline); err != nil {
				t.Fatal(err)
			}
			u := newAppServerSessionTestUI(t, d.workspace)
			u.thread, u.reset = d.thread, d
			u.session.start(d.thread, d.workspace)
			u.view.painter.Theme = livediff.DarkTheme
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": d.thread, "turn": map[string]any{"id": "fallback-turn"}})
			resetWordingCompaction(t, d, true)
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": d.thread, "turnId": "fallback-turn", "item": appServerItem{ID: "fallback", Type: "contextCompaction"}})
			// Review the completed event, not the still-pending driver's strip.
			u.turn, u.status, u.reset = "", "Ready", nil
			rows, _ := u.mainFrame(100, 16, 0)
			assertNativeUISnapshot(t, "native-reset-provider-fallback-"+mode, rows)
		})
	}
}
