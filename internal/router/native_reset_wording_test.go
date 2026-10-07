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
		if err := d.proxy.journals.transaction(t.Context(), d.proxy.replayStore, d.workspace, d.thread, func(j *threadJournal, _ bool) error {
			j.IdentityConflicted = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	request, headers := journalCompactionRequest(t, d.workspace, d.thread)
	metadata, _ := decodeCodexTurnMetadata(headers)
	metadata.TurnID = d.compactTurn
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

// Isolate the reset presentation from the fixture's earlier task milestones,
// and fix the durable event timestamp before rendering it.
func applyResetWordingJournal(u *appServerUI) {
	for _, p := range u.journal.snapshot() {
		if p.event != nil && p.event.Op == "reset" {
			event := *p.event
			event.At = u.view.now().Format(time.RFC3339Nano)
			p.event = &event
			u.applyJournalPublication(u.journal, p)
		}
	}
}

func fixResetPresentationTime(u *appServerUI) {
	for i := range u.view.entries {
		u.view.mutateEntry(i, func(record *liveActivityRecord) {
			record.Observed = u.view.now()
			if record.journalEvent != nil {
				event := *record.journalEvent
				event.At = record.Observed.Format(time.RFC3339Nano)
				record.journalEvent = &event
			}
		})
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
			u.journal = d.proxy.journals.attachNative(d.workspace, d.thread)
			t.Cleanup(func() { d.proxy.journals.detachNative(u.journal) })
			if err := d.proxy.journals.restoreNative(t.Context(), d.proxy.replayStore, u.journal); err != nil {
				t.Fatal(err)
			}
			u.view.painter.Theme = livediff.DarkTheme
			u.view.clock = func() time.Time { return now }
			u.clock = u.view.clock
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
			if err := d.continuePlan(true); err != nil {
				t.Fatal(err)
			}
			applyResetWordingJournal(u)
			// Keep ordinary provider compaction wording even in reset-enabled sessions.
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": d.thread, "turnId": "provider-turn", "item": map[string]any{"id": "provider-item", "type": "contextCompaction"}})
			u.turn = "" // No duration-dependent status in the completion frame.
			fixResetPresentationTime(u)
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
	u.proxy = &mekugiProxy{replayStore: reopened}
	u.session.start(d.thread, d.workspace)
	store := newJournalStore()
	u.journal = store.attachNative(d.workspace, d.thread)
	if err := store.restoreNative(t.Context(), reopened, u.journal); err != nil {
		t.Fatal(err)
	}
	u.view.painter.Theme = livediff.DarkTheme
	u.view.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
	u.restoreHistory([]appServerHistoryTurn{
		{ID: "reset-turn", Status: "completed", Items: []appServerItem{{ID: "reset", Type: "contextCompaction"}}},
		{ID: "provider-turn", Status: "completed", Items: []appServerItem{{ID: "provider", Type: "contextCompaction"}}},
	})
	applyResetWordingJournal(u)
	fixResetPresentationTime(u)
	rows, _ := u.mainFrame(100, 16, 0)
	assertNativeUISnapshot(t, "native-reset-restored", rows)
	if u.journalResetTurn("child", "reset-turn") || u.journalResetTurn(d.thread, "") {
		t.Fatal("reset identity leaked to another thread or unknown turn")
	}
}

func TestUISnapshotNativeResetProviderFallback(t *testing.T) {
	for _, mode := range []string{"slice"} {
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

func TestUISnapshotNativeAutoCompactionWording(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	proxy.journalCompaction = "auto"
	d := &journalResetDriver{ctx: t.Context(), proxy: proxy, workspace: workspace, thread: transform.shellThreadID}
	u := newAppServerSessionTestUI(t, d.workspace)
	u.thread, u.proxy = d.thread, d.proxy
	u.session.start(d.thread, d.workspace)
	u.journal = d.proxy.journals.attachNative(d.workspace, d.thread)
	t.Cleanup(func() { d.proxy.journals.detachNative(u.journal) })
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	u.view.painter.Theme = livediff.DarkTheme
	u.view.clock = func() time.Time { return now }
	u.clock = u.view.clock
	// Ordinary auto compactions have no slice-reset driver or armed intent.
	for _, turn := range []string{"auto-first", "auto-second", "provider-fallback"} {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": d.thread, "turn": map[string]any{"id": turn}})
		item := appServerItem{ID: turn + "-item", Type: "contextCompaction"}
		appServerTestNotify(t, u, "item/started", map[string]any{"threadId": d.thread, "turnId": turn, "item": item})
		u.turnStarted = now
		if turn == "auto-first" {
			assertNativeUISnapshot(t, "native-auto-compaction-working", []string{u.sessionLabel(now)})
		}
		d.compactTurn = turn
		if turn == "provider-fallback" {
			// An older provider answer remains readable after opting into auto.
			proxy.journalCompaction = "off"
		}
		resetWordingCompaction(t, d, turn == "provider-fallback")
		proxy.journalCompaction = "auto"
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": d.thread, "turnId": turn, "item": item})
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": d.thread, "turnId": "auto-first", "item": appServerItem{ID: "same-turn-provider", Type: "contextCompaction"}})
	u.turn, u.status = "", "Ready"
	fixResetPresentationTime(u)
	rows, _ := u.mainFrame(100, 16, 0)
	assertNativeUISnapshot(t, "native-auto-compaction-completed", rows)

	// Restore only this thread in a fresh store and view, without a live driver.
	reopened, err := openMekugiReplayStore(d.proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	restored := newAppServerSessionTestUI(t, d.workspace)
	restored.thread = d.thread
	restored.proxy = &mekugiProxy{replayStore: reopened, journalCompaction: "auto"}
	restored.session.start(d.thread, d.workspace)
	restored.journal = &nativeJournalSink{workspace: d.workspace, thread: d.thread}
	restored.view.painter.Theme = livediff.DarkTheme
	restored.view.clock = u.view.clock
	var turns []appServerHistoryTurn
	for _, turn := range []string{"auto-first", "auto-second", "provider-fallback"} {
		turns = append(turns, appServerHistoryTurn{ID: turn, Status: "completed", Items: []appServerItem{{ID: turn + "-item", Type: "contextCompaction"}}})
	}
	turns[0].Items = append(turns[0].Items, appServerItem{ID: "same-turn-provider", Type: "contextCompaction"})
	restored.restoreHistory(turns)
	fixResetPresentationTime(restored)
	rows, _ = restored.mainFrame(100, 16, 0)
	assertNativeUISnapshot(t, "native-auto-compaction-restored", rows)
	if restored.journalCompactionAnswered("child", "auto-first", "auto-first-item") || restored.journalCompactionAnswered(d.thread, "", "auto-first-item") || restored.journalCompactionAnswered(d.thread, "provider-fallback", "provider-fallback-item") || restored.journalCompactionAnswered(d.thread, "auto-first", "different-item") {
		t.Fatal("journal compaction receipt leaked to another thread, unknown turn or provider fallback")
	}
}

func TestUISnapshotNativeCompactCommand(t *testing.T) {
	for _, mode := range []string{"auto", "slice", "off"} {
		u, _ := newAppServerTestUI()
		u.proxy = &mekugiProxy{journalCompaction: mode}
		u.view.painter.Theme = livediff.DarkTheme
		u.picker.open = true
		u.filterCommands("compact")
		assertNativeUISnapshot(t, "native-compact-command-"+mode, u.renderPicker(88, 5))
	}
}

func TestNativeAutoCompactionDelayedNotification(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	proxy.journalCompaction = "auto"
	d := &journalResetDriver{ctx: t.Context(), proxy: proxy, workspace: workspace, thread: transform.shellThreadID, compactTurn: "delayed-manual"}
	u := newAppServerSessionTestUI(t, workspace)
	u.thread, u.proxy = d.thread, proxy
	u.session.start(u.thread, workspace)
	u.journal = proxy.journals.attachNative(workspace, u.thread)
	t.Cleanup(func() { proxy.journals.detachNative(u.journal) })
	resetWordingCompaction(t, d, false)
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": d.thread, "turn": map[string]any{"id": d.compactTurn}})
	item := appServerItem{ID: "delayed-item", Type: "contextCompaction"}
	for _, method := range []string{"item/started", "item/completed"} {
		appServerTestNotify(t, u, method, map[string]any{"threadId": d.thread, "turnId": d.compactTurn, "item": item})
	}
	if !proxy.replayStore.answeredCompactionItem(t.Context(), workspace, u.thread, d.compactTurn, item.ID) {
		t.Fatal("buffered standalone notifications did not durably bind exact item identity")
	}
	if text, _, _ := u.progress(item, "item/completed", d.thread, d.compactTurn); text != "Context reset from journal" {
		t.Fatalf("late host item lost journal-reset presentation: %q", text)
	}
	if text, _, _ := u.progress(appServerItem{ID: "other", Type: "contextCompaction"}, "item/completed", d.thread, d.compactTurn); text != "Context reset" {
		t.Fatalf("standalone receipt relabeled another host item: %q", text)
	}
}

func TestNativeAutomaticCompactionBufferedPreviousItem(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	proxy.journalCompaction = "auto"
	d := &journalResetDriver{ctx: t.Context(), proxy: proxy, workspace: workspace, thread: transform.shellThreadID, compactTurn: "ordinary-turn"}
	u := newAppServerSessionTestUI(t, workspace)
	u.thread, u.proxy = d.thread, proxy
	u.session.start(u.thread, workspace)
	u.journal = proxy.journals.attachNative(workspace, u.thread)
	t.Cleanup(func() { proxy.journals.detachNative(u.journal) })
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": u.thread, "turn": map[string]any{"id": d.compactTurn}})
	old := appServerItem{ID: "prior-provider-item", Type: "contextCompaction"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": u.thread, "turnId": d.compactTurn, "item": old})
	// The prior provider completion and next automatic start are buffered in
	// the native client; the router already receives the next compaction HTTP request.
	request, headers := journalCompactionRequest(t, workspace, u.thread)
	metadata, _ := decodeCodexTurnMetadata(headers)
	metadata.TurnID = d.compactTurn
	metadata.Compaction = mustTestJSON(t, map[string]any{"trigger": "auto", "reason": "context_limit", "phase": "mid_turn", "implementation": "responses_compaction_v2", "strategy": "memento"})
	headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
	provider := &serverFakeProvider{}
	if err := executeRequest(t.Context(), t.Context(), request, headers, "compact", provider, io.Discard, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 0 {
		t.Fatal("expected router journal answer")
	}
	if text, _, _ := u.progress(old, "item/completed", u.thread, d.compactTurn); text != "Context reset" {
		t.Fatalf("prior provider item incorrectly relabeled after a later journal answer: %q", text)
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": u.thread, "turnId": d.compactTurn, "item": old})
	next := appServerItem{ID: "next-journal-item", Type: "contextCompaction"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": u.thread, "turnId": d.compactTurn, "item": next})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": u.thread, "turnId": d.compactTurn, "item": next})
	reopened, err := openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	restored := newAppServerSessionTestUI(t, workspace)
	restored.thread = u.thread
	restored.proxy = &mekugiProxy{replayStore: reopened, journalCompaction: "auto"}
	restored.journal = &nativeJournalSink{workspace: workspace, thread: u.thread}
	restored.session.start(u.thread, workspace)
	restored.restoreHistory([]appServerHistoryTurn{{ID: d.compactTurn, Status: "completed", Items: []appServerItem{old, next}}})
	for _, view := range []*appServerUI{u, restored} {
		for _, item := range []appServerItem{old, next} {
			if text, _, _ := view.progress(item, "item/completed", u.thread, d.compactTurn); text != "Context reset" {
				t.Fatalf("ambiguous automatic item %s relabeled: %q", item.ID, text)
			}
		}
	}
}
