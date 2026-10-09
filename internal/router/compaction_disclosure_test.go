package router

import (
	"slices"
	"testing"
	"time"
)

func TestUISnapshotAutomaticResetRolloutDisclosure(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	proxy.journalCompaction = "auto"
	_, recovery := deliverV2ContinuityReset(t, proxy, workspace, thread)
	turn := recovery.Turn
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "log", Text: new("Newer state must not replace original recovery")}}); err != nil {
		t.Fatal(err)
	}
	deliverV2ContinuityReset(t, proxy, workspace, thread)
	completed := func(item string) map[string]any {
		return map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "thread_id": thread, "turn_id": turn, "item": map[string]any{"type": "ContextCompaction", "id": item}}}
	}
	info := appServerThreadInfo{ID: thread, Cwd: workspace, Path: writeTestRollout(t, thread,
		map[string]any{"type": "compacted", "payload": map[string]any{"compaction_response_id": "provider-response"}}, completed("provider-item"),
		map[string]any{"type": "compacted", "payload": map[string]any{"compaction_response_id": recovery.ResponseID}}, completed("journal-item"))}
	for _, restored := range []bool{true, false} {
		u := newAppServerSessionTestUI(t, workspace)
		u.view.clock = func() time.Time { return time.Date(2026, 9, 30, 20, 0, 0, 0, time.Local) }
		u.clock = u.view.clock
		u.thread, u.proxy = thread, proxy
		if restored {
			u.proxy = reopenV2ContinuityProxy(t, proxy)
		}
		u.session.start(thread, workspace)
		u.restoreContextUsage(u.session.agent("/root"), info)
		u.journal = &nativeJournalSink{workspace: workspace, thread: thread}
		items := []appServerItem{{ID: "provider-item", Type: "contextCompaction"}, {ID: "journal-item", Type: "contextCompaction"}}
		if restored {
			u.restoreHistory([]appServerHistoryTurn{{ID: turn, Status: "completed", Items: items}})
		} else {
			for _, item := range items {
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": turn, "item": item})
			}
		}
		fixResetPresentationTime(u)
		feed := u.view.renderFeed(90, 20)
		row := slices.IndexFunc(feed.snippets, func(s liveActivitySnippet) bool {
			b, ok := u.view.snippetBlock(s)
			return ok && b.Verb == "Journal recovery"
		})
		if row < 0 {
			t.Fatal("automatic journal reset has no recovery target")
		}
		assertNativeUISnapshot(t, "automatic-reset-rollout-disclosure", feed.lines)
		shell := selectionTestUI(feed.lines...)
		shell.main = u
		u.view.feedTop, u.view.feedLeft, u.view.feedRight, u.view.feedRows = 1, 1, 90, len(feed.lines)
		u.view.feedSnippets = feed.snippets
		if !shell.selectionMouse(0, 2, row, false) || !shell.selectionMouse(0, 2, row, true) || shell.output == nil || shell.output.pages[0].Body != recovery.Text {
			t.Fatal("automatic reset click lost the original recovery text")
		}
		if text, _, _ := u.progress(items[0], "item/completed", thread, turn); text != "Context reset" {
			t.Fatal("provider item acquired journal provenance")
		}
	}
	if rolloutCompactionResponse(appServerThreadInfo{ID: "foreign", Path: info.Path}, turn, "journal-item") != "" || rolloutCompactionResponse(info, "foreign-turn", "journal-item") != "" {
		t.Fatal("rollout provenance crossed identity boundaries")
	}
	info.Path = writeTestRollout(t, thread, map[string]any{"type": "compacted", "payload": map[string]any{"compaction_response_id": recovery.ResponseID}})
	if rolloutCompactionResponse(info, turn, "journal-item") != "" {
		t.Fatal("legacy rollout acquired exact item provenance")
	}
	info.Path = writeTestRollout(t, thread, completed("previous-item"), completed("journal-item"))
	if rolloutCompactionResponse(info, turn, "journal-item") != "" {
		t.Fatal("ambiguous item acquired another item's installed response")
	}
}
