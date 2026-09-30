package router

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/uisnapshot"
)

func newPagedHistoryUI(t *testing.T) (*appServerUI, *appServerTestInput) {
	t.Helper()
	u, wire := newAppServerTestUI()
	u.ctx = t.Context()
	u.agents = newLiveActivityView()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.session.start("main", t.TempDir())
	u.resumeThread = "main"
	if err := u.restorePaneContent(appServerThreadInfo{ID: "main", Cwd: u.session.cwd}); err != nil {
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

func entryTexts(v *liveActivityView) []string {
	var result []string
	for _, e := range v.entries {
		if e.Kind == "text" || e.Kind == "final" {
			result = append(result, e.Text)
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
	if u.restoring != nil || u.historyLoading != nil || u.agents.status != "" {
		t.Fatalf("partial history blocked resume: restoring=%+v loading=%+v status=%q agents=%q", u.restoring, u.historyLoading, u.status, u.agents.status)
	}
	agent := u.session.agent("/root/worker")
	if agent.Turns != 2 || agent.Started.Unix() != 90 || agent.LastResponse.Unix() != 110 || !agent.Final || agent.Responding {
		t.Fatalf("lost turn metadata: %+v", agent)
	}
	if got := strings.Join(entryTexts(u.agents), ","); got != "middle commentary,latest answer" {
		t.Fatal(got)
	}
	if err := u.loadOlderActivity(); err != nil {
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
	if got := strings.Join(entryTexts(u.agents), ","); got != "early commentary,middle commentary,latest answer" {
		t.Fatal(got)
	}
	for _, entry := range u.agents.entries {
		if entry.Text == "early commentary" && (entry.Kind == "final" || entry.Observed.UnixMilli() != 101000) {
			t.Fatalf("incorrect older state: %+v", entry)
		}
	}
	if err := u.loadOlderActivity(); err != nil {
		t.Fatal(err)
	}
	requests = restoreContentRequests(t, wire)
	if requests[6].Params["turnId"] != "old" || requests[6].Params["cursor"] != nil {
		t.Fatalf("old turn not loaded independently: %+v", requests[6])
	}
	restoreContentReply(t, u, 6, map[string]any{"data": []any{}})
	if u.historyTarget() != nil || !u.session.agent("/root/worker").Final {
		t.Fatal("older failure replaced latest status or left false partial state")
	}
	if len(u.agents.entries) != 4 || u.agents.entries[0].Kind != "error" {
		t.Fatalf("old failed turn not ordered first: %+v", u.agents.entries)
	}
}

func TestAppServerAnchoredHistoryErrorsAndCancellation(t *testing.T) {
	for _, mode := range []string{"error", "invalid", "scope", "cursor", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			u, wire := newPagedHistoryUI(t)
			historyTurnsReply(t, u)
			restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("middle", "saved", 105000)}, "nextCursor": "older"})
			if err := u.loadOlderActivity(); err != nil {
				t.Fatal(err)
			}
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "live", "status": "inProgress"}})
			appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": "child", "turnId": "live", "itemId": "live-answer", "delta": "not buffered"})
			if len(u.historyLoading.pending) != 1 {
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
				u.shell.focus = 2
				u.agents.following = false
				if err := u.shell.send("\x1b"); err != nil {
					t.Fatal(err)
				}
				restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("first", "late response", 101000)}})
			}
			if u.historyLoading != nil || !u.session.agent("/root/worker").Responding || strings.Contains(strings.Join(entryTexts(u.agents), ","), "leak") || strings.Contains(strings.Join(entryTexts(u.agents), ","), "late response") {
				t.Fatal("failure/cancel lost live status or admitted stale data")
			}
			if err := u.loadOlderActivity(); err != nil {
				t.Fatal(err)
			}
			if len(restoreContentRequests(t, wire)) != 7 {
				t.Fatalf("history could not be retried: requests=%+v restoring=%+v loading=%+v", restoreContentRequests(t, wire), u.restoring, u.historyLoading)
			}
			restoreContentReply(t, u, 6, map[string]any{"data": []any{historyItem("first", "retry recovered", 101000)}})
			if !u.session.agent("/root/worker").Responding {
				t.Fatal("older retry overwrote live lifecycle")
			}
		})
	}
}

func TestAppServerAnchoredHistoryDeferredEvidence(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	h := u.historyLoading
	h.placements = []*restoredPlacement{
		{turn: "t", anchor: "first", at: time.Unix(102, 0), entry: activityPaneEntry{Agent: "/root/worker", Kind: "error", Text: "old cell failure"}},
		{turn: "t", anchor: "middle", at: time.Unix(106, 0), entry: activityPaneEntry{Agent: "/root/worker", Kind: "error", Text: "new cell failure"}},
		{at: time.Unix(103, 0), entry: activityPaneEntry{Agent: "/root/worker", Kind: "reply", Text: "time placed message"}},
	}
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", "latest", 109000), historyItem("middle", "middle", 105000)}, "nextCursor": "older"})
	if len(h.placements) != 2 || len(u.agents.entries) != 3 {
		t.Fatalf("older rollout evidence shown early or lost: %+v", u.agents.entries)
	}
	if err := u.loadOlderActivity(); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("first", "first", 101000)}})
	if len(h.placements) != 0 {
		t.Fatal("rollout evidence not drained at oldest page")
	}
	var got []string
	for _, e := range u.agents.entries {
		got = append(got, e.Text)
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
	if u.session.agent("/root/worker").Turns != 2 || u.session.agent("/root/worker").Final {
		t.Fatal("duplicate turns or interrupted status lost")
	}
	u.shell.focus = 2
	if err := u.shell.send("o"); err != nil {
		t.Fatal(err)
	}
	if len(restoreContentRequests(t, wire)) != 7 {
		t.Fatal("Activity key did not request older history")
	}
	if err := u.clearSessionPresentation(); err != nil {
		t.Fatal(err)
	}
	u.thread = "other"
	u.session.start("other", u.session.cwd)
	restoreContentReply(t, u, 6, map[string]any{"data": []any{historyItem("first", "foreign history", 101000)}})
	if len(u.agents.entries) != 0 || u.historyLoading != nil || u.childHistory != nil {
		t.Fatal("late old session response leaked across thread switch")
	}
}

func TestUISnapshotNativeAnchoredHistory(t *testing.T) {
	for _, state := range []string{"partial", "loading", "error"} {
		t.Run(state, func(t *testing.T) {
			u, _ := newPagedHistoryUI(t)
			historyTurnsReply(t, u)
			restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", "Latest retained answer", 109000)}, "nextCursor": "older"})
			u.shell.journalOpen = false
			u.shell.focus = 2
			if state != "partial" {
				if err := u.loadOlderActivity(); err != nil {
					t.Fatal(err)
				}
			}
			if state == "error" {
				restoreContentError(t, u, 5)
			}
			now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
			u.clock = func() time.Time { return now }
			for _, view := range []*liveActivityView{u.view, u.agents} {
				for i := range view.entries {
					view.entries[i].Observed = now.Add(-10 * time.Second)
				}
			}
			for i := range u.agents.agents {
				if u.agents.agents[i].Name != "/root" {
					u.agents.agents[i].Started = now.Add(-20 * time.Second)
					u.agents.agents[i].LastResponse = now.Add(-10 * time.Second)
				}
			}
			u.agents.clock = u.clock
			u.view.clock = u.clock
			u.shell.width, u.shell.height = 100, 24
			if err := u.shell.paintNative(t.Context(), io.Discard); err != nil {
				t.Fatal(err)
			}
			uisnapshot.Assert(t, "testdata/snapshots/native-anchored-history-"+state+".txt", strings.Join(u.shell.paintedRows, "\n")+"\n")
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
	if err := u.loadOlderActivity(); err != nil {
		t.Fatal(err)
	}
	requests := restoreContentRequests(t, wire)
	if requests[5].Params["cursor"] != "empty-continuation" {
		t.Fatal("empty page cannot continue without an anchor")
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "live", "item": map[string]any{"id": "live-answer", "type": "agentMessage", "text": "full live answer"}})
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "independent", "agentNickname": "independent", "agentRole": "worker"}})
	if u.session.paths["independent"] == "" {
		t.Fatal("another thread was blocked by older loading")
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("last", "historical commentary", 109000)}, "nextCursor": "older"})
	if got := strings.Join(entryTexts(u.agents), ","); got != "historical commentary,full live answer" {
		t.Fatal(got)
	}
	if u.agents.events["/root/worker"].seq != u.agents.entries[len(u.agents.entries)-1].Seq {
		t.Fatal("older history replaced latest live event")
	}
}

func TestAppServerAnchoredHistoryBufferSaturation(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", "saved", 109000)}, "nextCursor": "older"})
	if err := u.loadOlderActivity(); err != nil {
		t.Fatal(err)
	}
	for i := range 257 {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "live", "item": map[string]any{"id": fmt.Sprint(i), "type": "agentMessage", "text": "live"}})
	}
	if u.historyLoading != nil || len(u.agents.entries) != 258 {
		t.Fatal("buffer saturation lost live events or blocked the host")
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("first", "late ignored", 101000)}})
	if len(u.agents.entries) != 258 {
		t.Fatal("cancelled history request revived after overflow")
	}
}

func TestAppServerAnchoredHistoryRetainsPausedViewportAndLinks(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	u.agents.only = true
	u.agents.selected = "/root/worker"
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", "latest\nanswer", 109000), historyItem("middle", "middle\ncommentary", 105000)}, "nextCursor": "older"})
	u.agents.width = 60
	feed := u.agents.renderFeed(60, 3)
	u.agents.viewport(feed, 3)
	u.agents.following = false
	u.agents.offset = 3
	stable := u.agents.entries[0].Seq
	beforeRow := u.agents.questionRows[stable]
	beforeOffset := u.agents.offset
	if err := u.loadOlderActivity(); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("first", "older\ncommentary", 101000)}})
	if u.agents.entrySeq(activityPaneEntry{native: &liveActivityNativeItem{thread: "child", turn: "t", item: "middle"}}) != stable {
		t.Fatal("older insertion changed existing item links")
	}
	u.agents.renderFeed(60, 3)
	if u.agents.offset-u.agents.questionRows[stable] != beforeOffset-beforeRow || u.agents.following {
		t.Fatalf("paused viewport jumped: before=%d/%d after=%d/%d", beforeOffset, beforeRow, u.agents.offset, u.agents.questionRows[stable])
	}
	// A live update to a retained item still invalidates an out-of-order run.
	u.agents.runs = map[liveActivityRunKey]liveActivityRun{{first: u.agents.entries[0].Seq, last: u.agents.entries[len(u.agents.entries)-1].Seq}: {}}
	u.agents.invalidateEntry(stable)
	if len(u.agents.runs) != 0 {
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
	if u.restoring != nil || u.agents.status != "History incomplete" || len(u.agents.entries) != 1 || u.session.agent("/root/worker").Turns != 8 {
		t.Fatal("metadata limit pretended complete coverage or hid retained recent items")
	}
}

func TestAppServerAnchoredHistoryArchivedChild(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ctx = t.Context()
	u.agents = newLiveActivityView()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.session.start("main", t.TempDir())
	u.resumeThread = "main"
	if err := u.restorePaneContent(appServerThreadInfo{ID: "main", Cwd: u.session.cwd}); err != nil {
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
	if len(u.agents.entries) != 1 || u.agents.entries[0].Text != "archived answer" || u.session.agent("/root/worker").Responding {
		t.Fatal("archived history lost or revived a child")
	}
}

func TestAppServerAnchoredHistoryInitialFailureRetryKeepsEvidence(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	u.restoring.root.Turns = []appServerHistoryTurn{{ID: "root-turn", Status: "completed", StartedAt: 80, CompletedAt: 120}}
	h := u.historyLoading
	h.placements = []*restoredPlacement{{turn: "t", anchor: "last", at: time.Unix(109, 0), entry: activityPaneEntry{Agent: "/root/worker", Kind: "reply", Text: "retained directed message", message: &activityMessage{from: "/root/worker", to: "/root", text: "retained directed message"}}}}
	restoreContentError(t, u, 4)
	count := func() int {
		n := 0
		for _, e := range u.view.entries {
			if e.Text == "retained directed message" {
				n++
			}
		}
		return n
	}
	if count() != 1 || u.restoring != nil || u.historyTarget() == nil || u.agents.historyHint != "History unavailable · o retry" {
		t.Fatal("failed initial read lost evidence or its retry state")
	}
	if err := u.loadOlderActivity(); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("last", "latest answer", 109000)}})
	if count() != 1 || len(h.placements) != 0 || u.agents.status == "History incomplete" {
		t.Fatal("recovery duplicated Main evidence or left a false gap")
	}
	for _, e := range u.view.entries {
		if e.Text == "retained directed message" && e.activitySeq == 0 {
			t.Fatal("recovered Activity link missing")
		}
	}
	if u.agents.entries[0].Kind != "final" {
		t.Fatal("initial retry treated latest answer as older commentary")
	}
}

func TestAppServerAnchoredHistoryCancelledMetadataRetry(t *testing.T) {
	for _, saturation := range []bool{false, true} {
		t.Run(fmt.Sprint(saturation), func(t *testing.T) {
			u, _ := newPagedHistoryUI(t)
			restoreContentError(t, u, 3)
			if err := u.loadOlderActivity(); err != nil {
				t.Fatal(err)
			}
			if saturation {
				for i := range 257 {
					appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "live", "item": map[string]any{"id": fmt.Sprint(i), "type": "agentMessage", "text": "live"}})
				}
			} else if err := u.cancelOlderActivity(); err != nil {
				t.Fatal(err)
			}
			if u.historyTarget() == nil || u.historyLoading != nil {
				t.Fatal("metadata cancellation removed recovery eligibility")
			}
			if err := u.loadOlderActivity(); err != nil {
				t.Fatal(err)
			}
			restoreContentReply(t, u, 5, map[string]any{"data": []any{map[string]any{"id": "t", "status": "completed", "startedAt": 100, "completedAt": 110}}})
			restoreContentReply(t, u, 6, map[string]any{"data": []any{historyItem("last", "recovered latest", 109000)}})
			if u.historyLoading != nil || !strings.Contains(strings.Join(entryTexts(u.agents), ","), "recovered latest") {
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
	live := *u.session.agent("/root/worker")
	if err := u.loadOlderActivity(); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{map[string]any{"id": "old", "status": "completed", "startedAt": 90, "completedAt": 95}}})
	restoreContentReply(t, u, 6, map[string]any{"data": []any{historyItem("last", "old failed turn", 109000)}})
	after := u.session.agent("/root/worker")
	if after.Started != live.Started || after.LastResponse != live.LastResponse || after.Final != live.Final || after.Responding {
		t.Fatalf("stale metadata overwrote completed live evidence: before=%+v after=%+v", live, after)
	}
}

func TestAppServerAnchoredHistoryRebindsCumulativeAnswersChronologically(t *testing.T) {
	u, _ := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	u.restoring.root.Turns = []appServerHistoryTurn{{ID: "root", Status: "completed", StartedAt: 80, CompletedAt: 120}}
	h := u.historyLoading
	h.info.Turns[0].Status = "completed"
	questionA, questionB := "First assignment.", "Follow-up assignment."
	assignment := func(id, text, turn, anchor string, at int64) *restoredPlacement {
		return &restoredPlacement{turn: turn, anchor: anchor, at: time.Unix(at, 0), entry: activityPaneEntry{Agent: "/root/worker", Kind: "assignment", Text: text, assignment: &activityAssignment{id: id, from: "/root", to: "/root/worker", text: text}}}
	}
	h.placements = []*restoredPlacement{assignment("a", questionA, "old", "first", 92), assignment("b", questionB, "t", "middle", 106)}
	answer := func(items ...journalItem) string {
		var body strings.Builder
		body.WriteString("Journal result `/root/worker`")
		writeJournalItems(&body, items)
		return body.String()
	}
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", answer(journalItem{ID: "a", Question: questionA, Text: "Updated first answer."}, journalItem{ID: "b", Question: questionB, Text: "Follow-up answer."}), 109000)}, "nextCursor": "older"})
	if err := u.loadOlderActivity(); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("middle", "follow-up work", 105000)}})
	if err := u.loadOlderActivity(); err != nil {
		t.Fatal(err)
	}
	oldAnswer := historyItem("old-answer", answer(journalItem{ID: "a", Question: questionA, Text: "Original first answer."}), 98000)
	oldAnswer["turnId"] = "old"
	oldWork := historyItem("first", "first work", 91000)
	oldWork["turnId"] = "old"
	restoreContentReply(t, u, 6, map[string]any{"data": []any{oldAnswer, oldWork}})
	for _, v := range []*liveActivityView{u.agents, u.view} {
		targets := make(map[string]uint64)
		for _, e := range v.entries {
			if e.assignment != nil {
				targets[e.assignment.id] = e.Seq
			}
		}
		counts := make(map[string]int)
		for _, blocks := range v.blocks {
			for _, block := range blocks {
				if block.Journal == nil {
					continue
				}
				for _, group := range block.Journal.Groups {
					for _, a := range group.Answers {
						counts[a.ID]++
						if group.Target == 0 || group.Target != targets[a.ID] {
							t.Fatalf("answer did not acquire original assignment target: %+v targets=%+v", group, targets)
						}
						if a.ID == "a" && a.Text != "Updated first answer." {
							t.Fatalf("older snapshot overwrote the newer answer: %+v", a)
						}
					}
				}
			}
		}
		if counts["a"] != 1 || counts["b"] != 1 {
			t.Fatalf("cumulative snapshots repeated or lost answers: %+v", counts)
		}
	}
}
