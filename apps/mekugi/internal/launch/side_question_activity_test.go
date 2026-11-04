package launch

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func sideQuestionActivityItem(t *testing.T, status string) binding.ThreadItem {
	t.Helper()
	result, err := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": `{"thread_id":"worker-thread","task_name":"/root/worker","answer":"The **parser** tests pass."}`}}})
	if err != nil {
		t.Fatal(err)
	}
	item := binding.ThreadItem{ID: "side-question", Type: "mcpToolCall", Server: "threadcontrol", Tool: "send_message", Status: status,
		Arguments: []byte(`{"target":"worker","message":"Are the parser tests ready?","btw":true}`), Result: result}
	if status == "inProgress" {
		item.Result = nil
	} else if status == "failed" {
		item.Result = []byte(`{"isError":true,"content":[{"type":"text","text":"Target unavailable"}]}`)
	}
	return item
}

func TestSideQuestionActivityLiveRestoredReplay(t *testing.T) {
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			item := sideQuestionActivityItem(t, status)
			check := func(source *sessionpresentation.ActivitySource, painted string) {
				t.Helper()
				if len(source.Entries) != 1 || source.Entries[0].Source.Message != nil || source.Entries[0].Source.ActivitySeq != 0 {
					t.Fatalf("side question changed ordinary message identity: %+v", source.Entries)
				}
				entry := source.Entries[0]
				if entry.Source.Native.Item != item.ID || entry.Values[0].DetailCode == "" || strings.Contains(painted, "unconfirmed") || strings.Contains(painted, "MCP") {
					t.Fatalf("lost tool identity or semantic presentation: %+v\n%s", entry, painted)
				}
				if status == "failed" {
					if entry.Values[0].Kind != "error" || !strings.Contains(painted, "Side question worker failed: Target unavailable") {
						t.Fatalf("missing side failure: %+v\n%s", entry.Values, painted)
					}
					return
				}
				values := entry.Values
				if len(values) != 3 || values[0].Kind != "progress" || values[0].Live || !strings.Contains(values[0].Body, "answered") || values[1].From != "Main" || values[1].To != "/root/worker" || values[2].From != "/root/worker" || values[2].To != "Main" {
					t.Fatalf("missing settled directed exchange: %+v", values)
				}
				if !strings.Contains(painted, "Are the parser tests ready?") || !strings.Contains(painted, "The parser tests pass.") {
					t.Fatalf("missing rendered question/answer: %s", painted)
				}
			}
			for _, restored := range []bool{false, true} {
				u := newAppServerSessionTestUI(t, t.TempDir())
				if restored {
					u.restoreHistory([]binding.HistoryTurn{{ID: "turn", Status: "completed", Items: []binding.ThreadItem{item}}})
				} else {
					appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": sideQuestionActivityItem(t, "inProgress")})
					pending := u.attached.source.Entries[0]
					if len(pending.Values) != 2 || !strings.Contains(pending.Values[0].Body, "pending") {
						t.Fatalf("missing pending side question: %+v", pending)
					}
					seq := pending.Source.Seq
					appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
					if u.attached.source.Entries[0].Source.Seq != seq {
						t.Fatal("completion replaced host identity")
					}
				}
				check(u.attached.source, ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).testRenderConversation(100).Lines, "\n")))
			}
			path := replayTestWrite(t, t.TempDir(), "side.jsonl", replayTestMeta("main"), replayTestItem("main", "turn", replayTestEpoch, replayTestEpoch+1000, map[string]any{
				"type": "McpToolCall", "id": item.ID, "server": item.Server, "tool": item.Tool, "status": item.Status, "arguments": item.Arguments, "result": item.Result,
			}))
			source, err := replayReadTest(t, nil, t.Context(), path, "", 1)
			if err != nil {
				t.Fatal(err)
			}
			p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
			t.Cleanup(p.close)
			if err := p.advance(p.Until); err != nil {
				t.Fatal(err)
			}
			check(p.ui.source, replayPlaybackTestPaint(t, p, 100, 30))
		})
	}
}

func TestUISnapshotSideQuestionActivity(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	view := u.testActivity(u.shell.Transcript)
	view.Clock, view.Painter.Theme, view.Conversation = u.clock, diffrender.DarkTheme, true
	view.testAudience()
	for _, status := range []string{"completed", "failed"} {
		item := sideQuestionActivityItem(t, status)
		item.ID += status
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	}
	now = now.Add(time.Second)
	settleActivity(now, view)
	for _, width := range []int{38, 80} {
		uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/side-question-%d.txt", width), append(view.testRenderConversation(width).Lines, "plain after exchange"), width)
	}
}

func TestSideQuestionRosterActivity(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn"}})
	u.attached.session.Lifecycle("main").Responding = true
	for _, status := range []string{"inProgress", "completed"} {
		method, state := "item/started", "pending"
		if status == "completed" {
			method, state = "item/completed", "answered"
		}
		appServerTestNotify(t, u, method, map[string]any{"threadId": "main", "turnId": "turn", "item": sideQuestionActivityItem(t, status)})
		u.attached.childSource.Apply(sessionpresentation.ActivityEvent{Agents: u.attached.session.Roster()})
		publication := u.attached.childSource.Publication(u.attached.presentation, u.attached.source)
		if len(publication.MainSummary) != 1 || publication.MainSummary[0].StatusAnimating() != (status == "inProgress") {
			t.Fatalf("%s: lost Main summary animation: %+v", status, publication.MainSummary)
		}
		roster := ansi.Strip(strings.Join(u.testActivity(u.shell.Agents).NativeRoster(140, 6, presentationTime(u.clock), true), "\n"))
		if !strings.Contains(roster, "Side question") || !strings.Contains(roster, state) || strings.Contains(roster, "→") || strings.Contains(roster, "parser tests") {
			t.Fatalf("%s: roster showed the exchange instead of its status:\n%s", status, roster)
		}
	}
}
