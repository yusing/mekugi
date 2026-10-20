package launch

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestMCPActivityLiveAndRestored(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		for _, status := range []string{"completed", "failed"} {
			t.Run(thread+"/"+status, func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				item := binding.ThreadItem{ID: "call", Type: "mcpToolCall", Server: "catalog", Tool: "lookup", Status: "inProgress", Arguments: jsontext.Value(`{"id":9007199254740993}`)}
				notify := func(method string) {
					appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "turn", "item": item})
				}
				notify("item/started")
				source := u.attached.source
				if thread == "child" {
					source = u.attached.childSource
				}
				if len(source.Entries) != 1 || !source.Entries[0].Source.Native.Running || source.Entries[0].Values[0].Verb != "MCP" {
					t.Fatalf("missing MCP start: %+v", source.Entries)
				}
				seq := source.Entries[0].Source.Seq
				item.Status, item.DurationMS = status, new(int64(125))
				item.Result = jsontext.Value(`{"content":[{"type":"text","text":"found"}],"structuredContent":{"id":9007199254740993}}`)
				if status == "failed" {
					item.Result = nil
					item.Error = jsontext.Value(`{"message":"lookup failed"}`)
				}
				notify("item/completed")
				notify("item/completed")
				item.Status = "inProgress"
				notify("item/started")
				if len(source.Entries) != 1 {
					t.Fatalf("duplicate MCP row: %d", len(source.Entries))
				}
				entry := source.Entries[0]
				if entry.Source.Seq != seq || entry.Source.Native.Thread != thread || entry.Source.Native.Turn != "turn" || entry.Source.Native.Running || entry.Source.Native.Status != status || len(u.attached.session.Commands()) != 0 {
					t.Fatalf("wrong completion identity/state: %+v", entry.Source)
				}
				block := entry.Values[0]
				if block.Running || block.Duration != 125*time.Millisecond || !strings.Contains(block.Label, "catalog") || !strings.Contains(block.Label, "lookup") {
					t.Fatalf("lost MCP facts: %+v", block)
				}
				var details struct {
					Arguments jsontext.Value `json:"arguments"`
					Result    jsontext.Value `json:"result"`
					Error     jsontext.Value `json:"error"`
				}
				if err := json.Unmarshal([]byte(block.Code), &details); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(details.Arguments, item.Arguments) || !bytes.Equal(details.Result, item.Result) || !bytes.Equal(details.Error, item.Error) {
					t.Fatalf("lost tool detail: %s", block.Code)
				}
				item.Status = status
				restored := newAppServerSessionTestUI(t, t.TempDir())
				var entries []sessionpresentation.ActivityEntry
				restoreActivityItem(&restored.attached, restored.shell, restored.clock, restored.ctx, restored.retained, restored.retainedSession, restored.activity, restored.segments, &restored.restoredSegments, binding.ThreadInfo{ID: thread, Cwd: restored.attached.cwd}, binding.HistoryTurn{ID: "turn"}, item, entry.Source.Agent, time.Unix(100, 0), &entries, nil)
				if len(entries) != 1 || entries[0].Native.Live || entries[0].Native.Running || entries[0].Text != entry.Source.Text {
					t.Fatalf("restoration lost MCP evidence: %+v", entries)
				}
			})
		}
	}
}

func TestMCPActivityReplay(t *testing.T) {
	path := replayTestWrite(t, t.TempDir(), "mcp.jsonl", replayTestMeta("main"), replayTestItem("main", "turn", replayTestEpoch, replayTestEpoch+1000, map[string]any{
		"type": "McpToolCall", "id": "call", "server": "catalog", "tool": "lookup", "status": "failed", "arguments": map[string]any{"id": 42}, "result": map[string]any{"content": []any{}, "structuredContent": map[string]any{"ok": false}}, "error": map[string]any{"message": "lookup failed"},
	}))
	source, err := replayReadTest(t, nil, t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	if err := p.advance(500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	before := replayPlaybackTestPaint(t, p, 80, 30)
	if !strings.Contains(before, "catalog") || strings.Contains(before, "lookup failed") || strings.Contains(before, "structuredContent") {
		t.Fatalf("replay start leaked outcome or lost identity:\n%s", before)
	}
	if err := p.advance(p.Until); err != nil {
		t.Fatal(err)
	}
	after := replayPlaybackTestPaint(t, p, 80, 30)
	if !strings.Contains(after, "MCP") || !strings.Contains(after, "failed") || len(p.ui.session.Commands()) != 0 {
		t.Fatalf("replay lost completion or revived execution:\n%s", after)
	}
}

func TestUISnapshotMCPActivity(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
		u.clock = func() time.Time { return now }
		view := u.testActivity(u.shell.Transcript)
		if thread == "child" {
			view = u.testActivity(u.shell.Agents)
		}
		view.Clock, view.Painter.Theme = u.clock, diffrender.DarkTheme
		view.Conversation = true
		view.testAudience()
		for _, status := range []string{"inProgress", "completed", "failed"} {
			item := binding.ThreadItem{ID: status, Type: "mcpToolCall", Server: "catalog", Tool: "lookup", Status: status, Arguments: jsontext.Value(`{"id":42}`)}
			method := "item/started"
			if status != "inProgress" {
				method = "item/completed"
				item.DurationMS = new(int64(125))
				item.Result = jsontext.Value(`{"content":[{"type":"text","text":"found"}]}`)
			}
			if status == "failed" {
				item.Result = nil
				item.Error = jsontext.Value(`{"message":"lookup failed"}`)
			}
			appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "turn", "item": item})
		}
		now = now.Add(time.Second)
		settleActivity(now, view)
		for _, width := range []int{38, 80} {
			rows := view.testRenderConversation(width).Lines
			blocks := view.blocks(2)
			if len(blocks) != 1 {
				t.Fatalf("missing failed MCP block: %+v", blocks)
			}
			page := view.Painter.DialogPage(blocks[0], width)
			rows = append(rows, "MCP details")
			for i := range page.Lines {
				rows = append(rows, page.Rows(i, width)...)
			}
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/mcp-%s-%d.txt", thread, width), append(rows, "plain after MCP"), width)
		}
	}
}
