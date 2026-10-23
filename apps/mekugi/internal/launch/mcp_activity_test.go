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
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
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
				restored.Thread = thread
				restored.startSession(thread, restored.attached.cwd)
				restored.restoreHistory([]binding.HistoryTurn{{ID: "turn", Status: "completed", Items: []binding.ThreadItem{item}}})
				entries := restored.attached.source.Entries
				if len(entries) != 1 || entries[0].Source.Native.Live || entries[0].Source.Native.Running || entries[0].Source.Text != entry.Source.Text {
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

func spawnActivityItem(status string) binding.ThreadItem {
	item := binding.ThreadItem{ID: "spawn", Type: "mcpToolCall", Server: "threadcontrol", Tool: "spawn_agent", Status: status,
		Arguments: jsontext.Value(`{"task_name":"worker","message":"Inspect the parser.","model":"requested-only"}`)}
	if status == "completed" {
		item.Result = jsontext.Value(`{"content":[{"type":"text","text":"{\"thread_id\":\"worker-thread\",\"task_name\":\"/root/worker\",\"state\":\"confirmed\"}"}]}`)
	} else if status == "failed" {
		item.Arguments = jsontext.Value(`{"task_name":"image-paths","message":"Inspect the parser."}`)
		item.Result = jsontext.Value(`{"isError":true,"content":[{"type":"text","text":"task name must contain only lowercase letters, digits and underscores"}]}`)
	}
	return item
}

func TestThreadControlSpawnActivityLiveAndRestored(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		for _, status := range []string{"completed", "failed"} {
			t.Run(thread+"/"+status, func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir())
				item := spawnActivityItem("inProgress")
				notify := func(method string) {
					appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "turn", "item": item})
				}
				notify("item/started")
				source := u.attached.source
				if thread == "child" {
					source = u.attached.childSource
				}
				if len(source.Entries) != 1 || source.Entries[0].Values[0].Kind != "progress" || !source.Entries[0].Source.Native.Running {
					t.Fatalf("missing pending spawn: %+v", source.Entries)
				}
				seq := source.Entries[0].Source.Seq
				item = spawnActivityItem(status)
				notify("item/completed")
				notify("item/completed")
				completed := item
				item = spawnActivityItem("inProgress")
				notify("item/started")
				check := func(source *sessionpresentation.ActivitySource) {
					t.Helper()
					if len(source.Entries) != 1 {
						t.Fatalf("duplicate spawn: %+v", source.Entries)
					}
					entry := source.Entries[0]
					if entry.Source.Native.Thread != thread || entry.Source.Native.Item != "spawn" || entry.Source.Native.Running || len(entry.Values) != 1 {
						t.Fatalf("lost spawn identity/outcome: %+v", entry.Source)
					}
					block := entry.Values[0]
					if block.Code != "" || !strings.Contains(block.DetailCode, `"arguments"`) || !strings.Contains(block.DetailCode, string(completed.Result)) {
						t.Fatalf("lost raw spawn details: %+v", block)
					}
					if status == "completed" {
						if block.Kind != "start" || block.Body != "Inspect the parser." || block.To != "/root/worker" || entry.Source.Start.Model != "" || entry.Source.Assignment == nil {
							t.Fatalf("lost semantic spawn: %+v", entry)
						}
						if thread == "main" && (block.From != "/root" || entry.Source.Agent != "Main") {
							t.Fatalf("spawn lost canonical Main attribution: %+v", entry)
						}
					} else if block.Kind != "error" || !strings.Contains(block.Body, "lowercase letters") || entry.Source.Start != nil || entry.Source.Assignment != nil {
						t.Fatalf("failed spawn invented delivery: %+v", entry)
					}
				}
				check(source)
				if source.Entries[0].Source.Seq != seq {
					t.Fatal("completion replaced source sequence")
				}
				restored := newAppServerSessionTestUI(t, t.TempDir())
				turns := []binding.HistoryTurn{{ID: "turn", Status: "completed", Items: []binding.ThreadItem{completed}}}
				restored.Thread = thread
				restored.startSession(thread, restored.attached.cwd)
				restored.restoreHistory(turns)
				check(restored.attached.source)
			})
		}
	}
}

func TestThreadControlSpawnRetainsObservedSettings(t *testing.T) {
	for _, model := range []string{"model", ""} {
		t.Run(model, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.attached.session.RegisterThread(observation.Thread{ID: "worker-thread", Path: "/root/worker"})
			settings := &u.attached.session.Agent("/root/worker").Settings
			settings.Model, settings.Effort, settings.Tier = model, "high", "fast"
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": spawnActivityItem("completed")})
			start := u.attached.source.Entries[0].Source.Start
			if start == nil || start.Model != model || start.Effort != "high" || start.Tier != "fast" {
				t.Fatalf("spawn lost observed settings: %+v", start)
			}
		})
	}
}

func TestUISnapshotThreadControlSpawn(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	view := u.testActivity(u.shell.Transcript)
	view.Clock, view.Painter.Theme, view.Conversation = u.clock, diffrender.DarkTheme, true
	view.testAudience()
	for _, status := range []string{"inProgress", "completed", "failed"} {
		item := spawnActivityItem(status)
		item.ID = status
		method := "item/completed"
		if status == "inProgress" {
			method = "item/started"
		}
		appServerTestNotify(t, u, method, map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	}
	now = now.Add(time.Second)
	settleActivity(now, view)
	for _, width := range []int{38, 80} {
		feed := view.testRenderConversation(width)
		for _, entry := range view.Entries {
			clickable := false
			for _, snippet := range feed.Snippets {
				if snippet.Run == entry.Source.Seq {
					block, ok := view.SnippetBlock(snippet)
					clickable = ok && block.DetailCode != ""
					if clickable {
						if page := view.Painter.DialogPage(block, width); page.Text != block.DetailCode {
							t.Fatalf("spawn %s copy drops displayed raw evidence", entry.Source.Native.Item)
						}
						break
					}
				}
			}
			if !clickable {
				t.Fatalf("spawn %s has no raw-detail click target", entry.Source.Native.Item)
			}
		}
		rows := feed.Lines
		blocks := view.blocks(2)
		if len(blocks) != 1 {
			t.Fatalf("missing failed spawn: %+v", blocks)
		}
		page := view.Painter.DialogPage(blocks[0], width)
		rows = append(rows, "Spawn details")
		for i := range page.Lines {
			rows = append(rows, page.Rows(i, width)...)
		}
		uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/threadcontrol-spawn-%d.txt", width), append(rows, "plain after spawn"), width)
	}
}

func TestThreadControlSpawnReplay(t *testing.T) {
	for _, status := range []string{"completed", "failed"} {
		item := spawnActivityItem(status)
		path := replayTestWrite(t, t.TempDir(), "spawn.jsonl", replayTestMeta("main"), replayTestItem("main", "turn", replayTestEpoch, replayTestEpoch+1000, map[string]any{
			"type": "McpToolCall", "id": item.ID, "server": item.Server, "tool": item.Tool, "status": item.Status, "arguments": item.Arguments, "result": item.Result,
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
		if before := replayPlaybackTestPaint(t, p, 80, 30); !strings.Contains(before, "Spawning") || strings.Contains(before, "started") || strings.Contains(before, "lowercase") {
			t.Fatalf("replay start lost pending state or leaked outcome:\n%s", before)
		}
		if err := p.advance(p.Until); err != nil {
			t.Fatal(err)
		}
		after := replayPlaybackTestPaint(t, p, 80, 30)
		if strings.Contains(after, "MCP") || strings.Contains(after, "started") != (status == "completed") || strings.Contains(after, "lowercase") != (status == "failed") {
			t.Fatalf("replay lost spawn outcome (%s):\n%s", status, after)
		}
	}
}

func coordinationActivityItems(t *testing.T) []binding.ThreadItem {
	t.Helper()
	items := []binding.ThreadItem{spawnActivityItem("completed")}
	for _, operation := range []struct{ tool, args, result string }{
		{"followup_task", `{"target":"worker","message":"Check the tests too."}`, `{"id":"followup","state":"confirmed","thread_id":"worker-thread","task_name":"/root/worker"}`},
		{"send_message", `{"target":"worker","message":"The parser fixture is ready."}`, `{"id":"message","state":"confirmed","thread_id":"worker-thread","task_name":"/root/worker"}`},
		{"wait_agent", `{"target":["/root/worker"]}`, `{"id":"wait","state":"registered"}`},
		{"interrupt_agent", `{"target":"worker"}`, `{"id":"interrupt","state":"confirmed","thread_id":"worker-thread","task_name":"/root/worker"}`},
		{"list_agents", `{}`, `{"agents":[{"agent_name":"/root"},{"agent_name":"/root/worker"}],"effects":[]}`},
	} {
		result, err := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": operation.result}}})
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, binding.ThreadItem{ID: operation.tool, Type: "mcpToolCall", Server: "threadcontrol", Tool: operation.tool, Status: "completed", Arguments: jsontext.Value(operation.args), Result: result})
	}
	return items
}

func TestThreadControlTrafficLiveRestoredReplay(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	items := coordinationActivityItems(t)
	for _, item := range items {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	}
	check := func(source *sessionpresentation.ActivitySource) {
		t.Helper()
		if len(source.Entries) != len(items) {
			t.Fatalf("missing operations: %+v", source.Entries)
		}
		for i, kind := range []string{"start", "message", "message", "progress", "progress", "progress"} {
			entry := source.Entries[i]
			if len(entry.Values) != 1 || entry.Values[0].Kind != kind || entry.Values[0].DetailCode == "" || entry.Publish().Hidden() {
				t.Fatalf("%s lost agent presentation: %+v", items[i].Tool, entry)
			}
			if i < 3 && (entry.Values[0].From != "/root" || entry.Values[0].To != "/root/worker") {
				t.Fatalf("%s lost canonical traffic: %+v", items[i].Tool, entry.Values)
			}
		}
		if wait := source.Entries[3].Values[0]; wait.Body != "Wait registered" || len(wait.WaitTargets) != 1 || wait.WaitTargets[0].Name != "/root/worker" {
			t.Fatalf("wait acknowledgement became completion: %+v", wait)
		}
	}
	check(u.attached.source)
	restored := newAppServerSessionTestUI(t, t.TempDir())
	restored.restoreHistory([]binding.HistoryTurn{{ID: "turn", Status: "completed", Items: items}})
	check(restored.attached.source)
	for _, item := range items {
		path := replayTestWrite(t, t.TempDir(), "coordination.jsonl", replayTestMeta("main"), replayTestItem("main", "turn", replayTestEpoch, replayTestEpoch+1000, map[string]any{
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
		if painted := replayPlaybackTestPaint(t, p, 80, 30); strings.Contains(painted, "MCP") || strings.Contains(painted, `"content"`) {
			t.Fatalf("%s replay exposed raw MCP:\n%s", item.Tool, painted)
		}
	}
}

func TestUISnapshotThreadControlTraffic(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	view := u.testActivity(u.shell.Transcript)
	view.Clock, view.Painter.Theme, view.Conversation = u.clock, diffrender.DarkTheme, true
	view.testAudience()
	for _, item := range coordinationActivityItems(t) {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	}
	now = now.Add(time.Second)
	settleActivity(now, view)
	for _, width := range []int{38, 80} {
		uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/threadcontrol-traffic-%d.txt", width), append(view.testRenderConversation(width).Lines, "plain after traffic"), width)
	}
}

func TestThreadControlChildMessageKeepsGlobalParent(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	item := coordinationActivityItems(t)[2]
	item.Arguments = jsontext.Value(`{"target":"/root","message":"The parser fixture is ready."}`)
	item.Result = jsontext.Value(`{"content":[{"type":"text","text":"{\"id\":\"message\",\"state\":\"confirmed\",\"thread_id\":\"parent-thread\",\"task_name\":\"/root\"}"}]}`)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	entry := u.attached.source.Entries[0].Publish()
	got := childEntries([]activity.Entry{entry}, "/root/worker")[0]
	if len(got.Values) != 1 || got.Values[0].From != "/root/worker" || got.Values[0].To != "/root" {
		t.Fatalf("child message became self-directed: %+v", got.Values)
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

// coordinationTestCall supplies the retained shape produced by threadcontrol.
func coordinationTestCall(t *testing.T, id, tool, thread, task, message string) binding.ThreadItem {
	t.Helper()
	args, err := json.Marshal(map[string]string{"task_name": task, "target": task, "message": message})
	if err != nil {
		t.Fatal(err)
	}
	acknowledgement, err := json.Marshal(map[string]string{"id": id, "state": "confirmed", "thread_id": thread, "task_name": task})
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": string(acknowledgement)}}})
	if err != nil {
		t.Fatal(err)
	}
	return binding.ThreadItem{ID: id, Type: "mcpToolCall", Server: "threadcontrol", Tool: tool, Status: "completed", Arguments: args, Result: result}
}
