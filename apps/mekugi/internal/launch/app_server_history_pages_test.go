package launch

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func newPagedHistoryUI(t *testing.T) (*appServerUI, *appServerTestInput) {
	t.Helper()
	u, wire := newAppServerTestUI()
	u.ctx = t.Context()

	t.Cleanup(func() { terminalTest(u).Diff.Close() })
	u.startSession("main", t.TempDir())
	u.Startup.Thread = "main"
	if err := restorePaneContent(&u.attached, u.shell, &u.Client, &u.restoring, u.review, u.reviewSource, u.preparePane, binding.ThreadInfo{ID: "main", Cwd: u.attached.cwd}); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 0, map[string]any{"data": []any{map[string]any{"id": "child", "agentNickname": "worker", "agentRole": "worker", "historyMode": "paginated"}}})
	// Archived descendants use the same bounded loader.
	restoreContentReply(t, u, 1, map[string]any{"data": []any{}})
	requests := restoreContentRequests(t, wire)
	if requests[2].Params["includeTurns"] != false {
		t.Fatalf("full history requested: %+v", requests[2])
	}
	restoreContentReply(t, u, 2, map[string]any{"thread": map[string]any{"id": "child", "historyMode": "paginated"}})
	return u, wire
}

func historyItem(id, text string, at int64) map[string]any {
	return map[string]any{"turnId": "t", "completedAtMs": at, "item": map[string]any{"type": "agentMessage", "id": id, "text": text}}
}

func historyTurnsReply(t *testing.T, u *appServerUI) {
	restoreContentReply(t, u, 3, map[string]any{"data": []any{map[string]any{"id": "t", "status": "completed", "startedAt": 100, "completedAt": 110}, map[string]any{"id": "old", "status": "failed", "startedAt": 90, "completedAt": 95, "error": map[string]any{"message": "retained failure"}}}})
}

func entryTexts(v *activityAdapter) []string {
	var result []string
	for _, e := range v.Entries {
		if e.Source.Kind == "text" || e.Source.Kind == "final" {
			result = append(result, e.Source.Text)
		}
	}
	return result
}

func TestAppServerAnchoredHistoryPages(t *testing.T) {
	u, wire := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	requests := restoreContentRequests(t, wire)
	if requests[3].Method != "thread/turns/list" || requests[3].Params["itemsView"] != "notLoaded" || requests[4].Params["turnId"] != "t" || requests[4].Params["limit"] != float64(100) {
		t.Fatalf("unbounded requests: %+v", requests)
	}
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", "latest answer", 109000), historyItem("middle", "middle commentary", 105000)}, "nextCursor": "older"})
	if u.restoring != nil || historyLoading(&u.attached, &u.childHistoryState) != nil || u.attached.childSource.Coverage != "" {
		t.Fatalf("partial history blocked resume: restoring=%+v loading=%+v status=%q agents=%q", u.restoring, historyLoading(&u.attached, &u.childHistoryState), u.attached.presentation.Status.Text, u.attached.childSource.Coverage)
	}
	agent := u.attached.session.Agent("/root/worker")
	if agent.Turns != 2 || agent.Started.Unix() != 90 || agent.LastResponse.Unix() != 110 || !agent.Final || agent.Responding {
		t.Fatalf("lost turn metadata: %+v", agent)
	}
	if got := strings.Join(entryTexts(u.testActivity(u.shell.Agents)), ","); got != "middle commentary,latest answer" {
		t.Fatal(got)
	}
	if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
		t.Fatal(err)
	}
	requests = restoreContentRequests(t, wire)
	cursor := requests[5].Params["cursor"].(map[string]any)
	if cursor["type"] != "item" || cursor["itemId"] != "middle" || requests[5].Params["turnId"] != "t" {
		t.Fatalf("wrong exclusive anchor: %+v", requests[5])
	}
	// Duplicate page boundary items do not duplicate entries or promote an
	// older commentary message to the latest completed turn's final answer.
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("middle", "middle commentary", 105000), historyItem("first", "early commentary", 101000)}})
	if got := strings.Join(entryTexts(u.testActivity(u.shell.Agents)), ","); got != "early commentary,middle commentary,latest answer" {
		t.Fatal(got)
	}
	for _, entry := range u.attached.childSource.Entries {
		if entry.Source.Text == "early commentary" && (entry.Source.Kind == "final" || entry.Source.Observed.UnixMilli() != 101000) {
			t.Fatalf("incorrect older state: %+v", entry)
		}
	}
	if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
		t.Fatal(err)
	}
	requests = restoreContentRequests(t, wire)
	if requests[6].Params["turnId"] != "old" || requests[6].Params["cursor"] != nil {
		t.Fatalf("old turn not loaded independently: %+v", requests[6])
	}
	restoreContentReply(t, u, 6, map[string]any{"data": []any{}})
	if historyLoadIntent(u).Thread != "" || !u.attached.session.Agent("/root/worker").Final {
		t.Fatal("older failure replaced latest status or left false partial state")
	}
	if len(u.attached.childSource.Entries) != 4 || u.attached.childSource.Entries[0].Source.Kind != "error" {
		t.Fatalf("old failed turn not ordered first: %+v", u.attached.childSource.Entries)
	}
}

func TestAppServerAnchoredHistoryErrorsAndCancellation(t *testing.T) {
	for _, mode := range []string{"error", "invalid", "scope", "cursor", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			u, wire := newPagedHistoryUI(t)
			historyTurnsReply(t, u)
			restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("middle", "saved", 105000)}, "nextCursor": "older"})
			if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
				t.Fatal(err)
			}
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "live", "status": "inProgress"}})
			appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": "child", "turnId": "live", "itemId": "live-answer", "delta": "not buffered"})
			if len(historyLoading(&u.attached, &u.childHistoryState).pending) != 1 {
				t.Fatal("same-child live lifecycle not buffered or deltas retained")
			}
			switch mode {
			case "error":
				restoreContentError(t, u, 5)
			case "invalid":
				appServerTestMessage(t, u, `{"id":6,"result":{"data":false}}`)
			case "scope":
				restoreContentReply(t, u, 5, map[string]any{"data": []any{map[string]any{"turnId": "foreign", "item": map[string]any{"id": "foreign", "type": "agentMessage", "text": "leak"}}}})
			case "cursor":
				restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("first", "leak", 101000)}, "nextCursor": "older"})
			case "cancel":
				terminalTest(u).Focus = 0
				u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/worker")
				u.shell.Agents.Following = false
				if err := terminalTest(u).send("\x1b"); err != nil {
					t.Fatal(err)
				}
				restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("first", "late response", 101000)}})
			}
			if historyLoading(&u.attached, &u.childHistoryState) != nil || !u.attached.session.Agent("/root/worker").Responding || strings.Contains(strings.Join(entryTexts(u.testActivity(u.shell.Agents)), ","), "leak") || strings.Contains(strings.Join(entryTexts(u.testActivity(u.shell.Agents)), ","), "late response") {
				t.Fatal("failure/cancel lost live status or admitted stale data")
			}
			if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
				t.Fatal(err)
			}
			if len(restoreContentRequests(t, wire)) != 7 {
				t.Fatalf("history could not be retried: requests=%+v restoring=%+v loading=%+v", restoreContentRequests(t, wire), u.restoring, historyLoading(&u.attached, &u.childHistoryState))
			}
			restoreContentReply(t, u, 6, map[string]any{"data": []any{historyItem("first", "retry recovered", 101000)}})
			if !u.attached.session.Agent("/root/worker").Responding {
				t.Fatal("older retry overwrote live lifecycle")
			}
		})
	}
}

func TestAppServerAnchoredHistoryDeferredEvidence(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	h := historyLoading(&u.attached, &u.childHistoryState)
	h.placements = []*restoredPlacement{
		{turn: "t", anchor: "first", at: time.Unix(102, 0), entry: sessionpresentation.ActivityEntry{Agent: "/root/worker", Kind: "error", Text: "old cell failure"}},
		{turn: "t", anchor: "middle", at: time.Unix(106, 0), entry: sessionpresentation.ActivityEntry{Agent: "/root/worker", Kind: "error", Text: "new cell failure"}},
		{at: time.Unix(103, 0), entry: sessionpresentation.ActivityEntry{Agent: "/root/worker", Kind: "reply", Text: "time placed message"}},
	}
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", "latest", 109000), historyItem("middle", "middle", 105000)}, "nextCursor": "older"})
	if len(h.placements) != 2 || len(u.attached.childSource.Entries) != 3 {
		t.Fatalf("older rollout evidence shown early or lost: %+v", u.attached.childSource.Entries)
	}
	if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("first", "first", 101000)}})
	if len(h.placements) != 0 {
		t.Fatal("rollout evidence not drained at oldest page")
	}
	var got []string
	for _, e := range u.attached.childSource.Entries {
		got = append(got, e.Source.Text)
	}
	if strings.Join(got, ",") != "first,old cell failure,time placed message,middle,new cell failure,latest" {
		t.Fatal(got)
	}
}

func TestAppServerAnchoredHistoryTurnPagesAndIsolation(t *testing.T) {
	u, wire := newPagedHistoryUI(t)
	restoreContentReply(t, u, 3, map[string]any{"data": []any{map[string]any{"id": "t", "status": "interrupted", "startedAt": 100, "completedAt": 110}}, "nextCursor": "next-turns"})
	restoreContentReply(t, u, 4, map[string]any{"data": []any{map[string]any{"id": "t", "status": "interrupted"}, map[string]any{"id": "old", "status": "completed", "startedAt": 90, "completedAt": 95}}})
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("last", "partial interrupted answer", 109000)}, "nextCursor": "older"})
	if u.attached.session.Agent("/root/worker").Turns != 2 || u.attached.session.Agent("/root/worker").Final {
		t.Fatal("duplicate turns or interrupted status lost")
	}
	terminalTest(u).Focus = 0
	u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/worker")
	if err := terminalTest(u).key(2); err != nil {
		t.Fatal(err)
	}
	if err := terminalTest(u).key('o'); err != nil {
		t.Fatal(err)
	}
	if len(restoreContentRequests(t, wire)) != 7 {
		t.Fatal("Activity key did not request older history")
	}
	replaceAppServerTestSession(t, u, "other")
	restoreContentReply(t, u, 6, map[string]any{"data": []any{historyItem("first", "foreign history", 101000)}})
	if len(u.attached.childSource.Entries) != 0 || historyLoading(&u.attached, &u.childHistoryState) != nil || len(slices.Collect(u.attached.session.Threads())) != 1 || u.attached.session.FindThread("other") == nil {
		t.Fatal("late old session response leaked across thread switch")
	}
}

func TestUISnapshotNativeAnchoredHistory(t *testing.T) {
	for _, state := range []string{"partial", "loading", "error"} {
		t.Run(state, func(t *testing.T) {
			u, _ := newPagedHistoryUI(t)
			historyTurnsReply(t, u)
			restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", "Latest retained answer", 109000)}, "nextCursor": "older"})

			terminalTest(u).Focus = 0
			u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/worker")
			if state != "partial" {
				if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
					t.Fatal(err)
				}
			}
			if state == "error" {
				restoreContentError(t, u, 5)
			}
			now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
			u.clock = func() time.Time { return now }
			for _, view := range []*activityAdapter{u.testActivity(u.shell.Transcript), u.testActivity(u.shell.Agents)} {
				for i := range view.Entries {
					view.Entries[i].Source.Observed = now.Add(-10 * time.Second)
				}
			}
			for i := range u.attached.childSource.Agents {
				if u.attached.childSource.Agents[i].Name != "/root" {
					u.attached.childSource.Agents[i].Started = now.Add(-20 * time.Second)
					u.attached.childSource.Agents[i].LastResponse = now.Add(-10 * time.Second)
				}
			}
			u.shell.Agents.Clock = u.clock
			u.shell.Transcript.Clock = u.clock
			terminalTest(u).Width, terminalTest(u).Height = 100, 24
			settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
			if err := terminalTest(u).paintNative(t.Context(), io.Discard); err != nil {
				t.Fatal(err)
			}
			uisnapshot.Assert(t, "testdata/snapshots/native-anchored-history-"+state+".txt", strings.Join(terminalTest(u).Frame.Rows, "\n")+"\n")
		})
	}
}

func TestAppServerAnchoredHistoryNoEffects(t *testing.T) {
	u, wire := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	restoreContentReply(t, u, 4, map[string]any{"data": []any{}})
	for _, request := range restoreContentRequests(t, wire) {
		if request.Method == "thread/resume" || request.Method == "turn/start" || request.Method == "thread/fork" {
			t.Fatalf("history caused effects: %+v", request)
		}
	}

}

func TestAppServerAnchoredHistoryEmptyPageAndLiveCompletion(t *testing.T) {
	u, wire := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	restoreContentReply(t, u, 4, map[string]any{"data": []any{}, "nextCursor": "empty-continuation"})
	if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
		t.Fatal(err)
	}
	requests := restoreContentRequests(t, wire)
	if requests[5].Params["cursor"] != "empty-continuation" {
		t.Fatal("empty page cannot continue without an anchor")
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "live", "item": map[string]any{"id": "live-answer", "type": "agentMessage", "text": "full live answer"}})
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "independent", "agentNickname": "independent", "agentRole": "worker"}})
	if u.attached.session.KnownCaller("independent") == "" {
		t.Fatal("another thread was blocked by older loading")
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("last", "historical commentary", 109000)}, "nextCursor": "older"})
	if got := strings.Join(entryTexts(u.testActivity(u.shell.Agents)), ","); got != "historical commentary,full live answer" {
		t.Fatal(got)
	}
	if u.shell.Agents.Events["/root/worker"].Seq != u.attached.childSource.Entries[len(u.attached.childSource.Entries)-1].Source.Seq {
		t.Fatal("older history replaced latest live event")
	}
}

func TestAppServerAnchoredHistoryBufferSaturation(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", "saved", 109000)}, "nextCursor": "older"})
	if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
		t.Fatal(err)
	}
	for i := range 257 {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "live", "item": map[string]any{"id": fmt.Sprint(i), "type": "agentMessage", "text": "live"}})
	}
	if historyLoading(&u.attached, &u.childHistoryState) != nil || len(u.attached.childSource.Entries) != 258 {
		t.Fatal("buffer saturation lost live events or blocked the host")
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("first", "late ignored", 101000)}})
	if len(u.attached.childSource.Entries) != 258 {
		t.Fatal("cancelled history request revived after overflow")
	}
}

func TestAppServerAnchoredHistoryRetainsPausedViewportAndLinks(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/worker")
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", "latest\nanswer", 109000), historyItem("middle", "middle\ncommentary", 105000)}, "nextCursor": "older"})
	u.shell.Agents.Width = 60
	feed := u.testActivity(u.shell.Agents).RenderFeed(60, 3)
	u.shell.Agents.Viewport(feed, 3)
	u.shell.Agents.Following = false
	u.shell.Agents.Offset = 3
	stable := u.attached.childSource.Entries[0].Source.Seq
	beforeRow := u.shell.Agents.QuestionRows[stable]
	beforeOffset := u.shell.Agents.Offset
	if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("first", "older\ncommentary", 101000)}})
	if u.attached.childSource.EntrySeq(sessionpresentation.ActivityEntry{Native: &sessionpresentation.NativeItem{Thread: "child", Turn: "t", Item: "middle"}}) != stable {
		t.Fatal("older insertion changed existing item links")
	}
	u.testActivity(u.shell.Agents).RenderFeed(60, 3)
	if u.shell.Agents.Offset-u.shell.Agents.QuestionRows[stable] != beforeOffset-beforeRow || u.shell.Agents.Following {
		t.Fatalf("paused viewport jumped: before=%d/%d after=%d/%d", beforeOffset, beforeRow, u.shell.Agents.Offset, u.shell.Agents.QuestionRows[stable])
	}
	// A live update to a retained item still invalidates an out-of-order run.
	u.shell.Agents.Runs = map[activityui.RunKey]activityui.Run{{First: u.attached.childSource.Entries[0].Source.Seq, Last: u.attached.childSource.Entries[len(u.attached.childSource.Entries)-1].Source.Seq}: {}}
	u.testActivity(u.shell.Agents).invalidateEntry(stable)
	if len(u.shell.Agents.Runs) != 0 {
		t.Fatal("older insertion broke live cache invalidation")
	}
}

func TestAppServerAnchoredHistoryTurnBudgetKeepsRecentItems(t *testing.T) {
	u, wire := newPagedHistoryUI(t)
	for i := range 8 {
		id := "t"
		if i > 0 {
			id = fmt.Sprintf("old-%d", i)
		}
		restoreContentReply(t, u, 3+i, map[string]any{"data": []any{map[string]any{"id": id, "status": "completed", "startedAt": 100 - int64(i)}}, "nextCursor": fmt.Sprint(i + 1)})
	}
	requests := restoreContentRequests(t, wire)
	if len(requests) != 12 || requests[11].Method != "thread/items/list" || requests[11].Params["turnId"] != "t" {
		t.Fatalf("metadata limit discarded recent history or kept paging: %+v", requests)
	}
	restoreContentReply(t, u, 11, map[string]any{"data": []any{historyItem("recent", "still available", 101000)}})
	if u.restoring != nil || u.attached.childSource.Coverage != "History incomplete" || len(u.attached.childSource.Entries) != 1 || u.attached.session.Agent("/root/worker").Turns != 8 {
		t.Fatal("metadata limit pretended complete coverage or hid retained recent items")
	}
}

func TestAppServerAnchoredHistoryArchivedChild(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ctx = t.Context()

	t.Cleanup(func() { terminalTest(u).Diff.Close() })
	u.startSession("main", t.TempDir())
	u.Startup.Thread = "main"
	if err := restorePaneContent(&u.attached, u.shell, &u.Client, &u.restoring, u.review, u.reviewSource, u.preparePane, binding.ThreadInfo{ID: "main", Cwd: u.attached.cwd}); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 0, map[string]any{"data": []any{}})
	restoreContentReply(t, u, 1, map[string]any{"data": []any{map[string]any{"id": "child", "parentThreadId": "main", "agentNickname": "worker", "agentRole": "worker", "historyMode": "paginated"}}})
	if restoreContentRequests(t, wire)[1].Params["archived"] != true || restoreContentRequests(t, wire)[2].Params["includeTurns"] != false {
		t.Fatal("archived child bypassed paginated loader")
	}
	restoreContentReply(t, u, 2, map[string]any{"thread": map[string]any{"id": "child", "historyMode": "paginated"}})
	historyTurnsReply(t, u)
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("archived-answer", "archived answer", 109000)}})
	if len(u.attached.childSource.Entries) != 1 || u.attached.childSource.Entries[0].Source.Text != "archived answer" || u.attached.session.Agent("/root/worker").Responding {
		t.Fatal("archived history lost or revived a child")
	}
}

func TestAppServerAnchoredHistoryInitialFailureRetryKeepsEvidence(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	u.restoring.root.Turns = []binding.HistoryTurn{{ID: "root-turn", Status: "completed", StartedAt: 80, CompletedAt: 120}}
	h := historyLoading(&u.attached, &u.childHistoryState)
	h.placements = []*restoredPlacement{{turn: "t", anchor: "last", at: time.Unix(109, 0), entry: sessionpresentation.ActivityEntry{Agent: "/root/worker", Kind: "reply", Text: "retained directed message", Message: &sessionpresentation.ActivityMessage{From: "/root/worker", To: "/root", Text: "retained directed message"}}}}
	restoreContentError(t, u, 4)
	count := func() int {
		n := 0
		for _, e := range u.attached.source.Entries {
			if e.Source.Text == "retained directed message" {
				n++
			}
		}
		return n
	}
	if err := u.testPaint(io.Discard, 100, 24); err != nil {
		t.Fatal(err)
	}
	if count() != 1 || u.restoring != nil || historyLoadIntent(u).Thread == "" || u.shell.Agents.HistoryHint != "History unavailable · Ctrl-B o retry" {
		t.Fatal("failed initial read lost evidence or its retry state")
	}
	if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("last", "latest answer", 109000)}})
	if count() != 1 || len(h.placements) != 0 || u.attached.childSource.Coverage == "History incomplete" {
		t.Fatal("recovery duplicated Main evidence or left a false gap")
	}
	for _, e := range u.attached.source.Entries {
		if e.Source.Text == "retained directed message" && e.Source.ActivitySeq == 0 {
			t.Fatal("recovered Activity link missing")
		}
	}
	if u.attached.childSource.Entries[0].Source.Kind != "final" {
		t.Fatal("initial retry treated latest answer as older commentary")
	}
}

func TestAppServerAnchoredHistoryCancelledMetadataRetry(t *testing.T) {
	for _, saturation := range []bool{false, true} {
		t.Run(fmt.Sprint(saturation), func(t *testing.T) {
			u, _ := newPagedHistoryUI(t)
			restoreContentError(t, u, 3)
			if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
				t.Fatal(err)
			}
			if saturation {
				for i := range 257 {
					appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "live", "item": map[string]any{"id": fmt.Sprint(i), "type": "agentMessage", "text": "live"}})
				}
			} else if err := u.cancelOlderActivity(historyCancelIntent(u)); err != nil {
				t.Fatal(err)
			}
			if historyLoadIntent(u).Thread == "" || historyLoading(&u.attached, &u.childHistoryState) != nil {
				t.Fatal("metadata cancellation removed recovery eligibility")
			}
			if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
				t.Fatal(err)
			}
			restoreContentReply(t, u, 5, map[string]any{"data": []any{map[string]any{"id": "t", "status": "completed", "startedAt": 100, "completedAt": 110}}})
			restoreContentReply(t, u, 6, map[string]any{"data": []any{historyItem("last", "recovered latest", 109000)}})
			if historyLoading(&u.attached, &u.childHistoryState) != nil || !strings.Contains(strings.Join(entryTexts(u.testActivity(u.shell.Agents)), ","), "recovered latest") {
				t.Fatal("metadata retry after cancellation did not recover")
			}
		})
	}
}

func TestAppServerAnchoredHistoryMetadataRetryPreservesCompletedLiveTiming(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	restoreContentReply(t, u, 3, map[string]any{"data": []any{map[string]any{"id": "t", "status": "failed", "startedAt": 100, "completedAt": 110}}, "nextCursor": "older-turns"})
	restoreContentError(t, u, 4)
	u.clock = func() time.Time { return time.Unix(200, 0) }
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "live", "status": "inProgress"}})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "live", "status": "completed"}})
	live := *u.attached.session.Agent("/root/worker")
	if err := loadOlderActivity(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, &u.statusPanel, &u.resumePicker, historyLoadIntent(u)); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{map[string]any{"id": "old", "status": "completed", "startedAt": 90, "completedAt": 95}}})
	restoreContentReply(t, u, 6, map[string]any{"data": []any{historyItem("last", "old failed turn", 109000)}})
	after := u.attached.session.Agent("/root/worker")
	if after.Started != live.Started || after.LastResponse != live.LastResponse || after.Final != live.Final || after.Responding {
		t.Fatalf("stale metadata overwrote completed live evidence: before=%+v after=%+v", live, after)
	}
}

func historyLoadIntent(u *appServerUI) sessionpresentation.HistoryIntent {
	v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	return sessionpresentation.HistoryIntent{Version: v.Version, Thread: v.History.Target}
}

func historyCancelIntent(u *appServerUI) sessionpresentation.HistoryIntent {
	v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	return sessionpresentation.HistoryIntent{Version: v.Version, Thread: v.Loading}
}
