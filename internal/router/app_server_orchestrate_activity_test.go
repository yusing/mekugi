package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/orchestrate"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotOrchestrateActivity(t *testing.T) {
	u, w := newAppServerTestUI()
	u.ctx = t.Context()
	u.session.start("main", t.TempDir())
	u.agents = newLiveActivityView()
	u.clock = func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.Local) }
	u.view.clock = u.clock
	u.view.painter.Theme = livediff.DarkTheme
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	item := map[string]any{"id": "spawn", "type": "mcpToolCall", "server": "orchestrate", "tool": "spawn_agent", "status": "inProgress", "arguments": map[string]any{"task_name": "batch", "message": "Complete the assignment."}}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	if len(u.view.entries) != 1 {
		t.Fatal("started call is missing", u.view.entries)
	}
	seq := u.view.entries[0].Seq
	rows, _ := u.mainFrame(90, 14, 0)
	assertNativeUISnapshot(t, "orchestration-call-started", rows)
	item["status"], item["durationMs"] = "failed", 211
	item["error"] = map[string]any{"message": "coordinator permission profile is unavailable; cannot inherit permissions"}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	if len(u.view.entries) != 1 || u.view.entries[0].Seq != seq || u.view.entries[0].blocks[0].Output == nil {
		t.Fatal("completion lost the call identity or error output")
	}
	rows, _ = u.mainFrame(90, 14, 0)
	assertNativeUISnapshot(t, "orchestration-call-failed", rows)
	if w.Len() != 0 || u.turn != "" {
		t.Fatal("display dispatched host work")
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{
		"id": "prepare", "type": "mcpToolCall", "server": "orchestrate", "tool": "prepare", "status": "completed",
		"arguments": map[string]any{"task_name": "batch"}, "durationMs": 80,
		"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "Batch prepared at the committed baseline."}}},
	}})
	rows, _ = u.mainFrame(90, 14, 0)
	assertNativeUISnapshot(t, "orchestration-call-completed", rows)

	// Restored Main and child items use the same block and output contract.
	for _, agent := range []string{"Main", "/root/batch"} {
		var entries []activityPaneEntry
		last := -1
		restore := func(item appServerItem, status string) {
			if agent == "Main" {
				u.view = newLiveActivityView()
				u.restoreHistoryItem(appServerHistoryTurn{ID: "turn", Status: status}, item)
				entries = nil
				for _, record := range u.view.entries {
					entries = append(entries, record.activityPaneEntry)
				}
			} else {
				u.restoreActivityItem(appServerThreadInfo{ID: "recorded"}, appServerHistoryTurn{ID: "turn", Status: status}, item, agent, time.Time{}, &entries, &last)
			}
		}
		var completed appServerItem
		if err := json.Unmarshal([]byte(`{"id":"prepare","type":"mcpToolCall","server":"orchestrate","tool":"prepare","status":"completed","arguments":{"task_name":"batch"},"result":{"content":[{"type":"text","text":"{\"task_name\":\"batch\",\"state\":\"prepared\"}"}]}}`), &completed); err != nil {
			t.Fatal(err)
		}
		restore(completed, "completed")
		if len(entries) != 1 || entries[0].Agent != agent || entries[0].native.running {
			t.Fatal("restored call is missing or revived work")
		}
		block := parseLiveActivity(entries[0])[0]
		if block.Output == nil || block.Output.View().Lines[0] != `{"task_name":"batch","state":"prepared"}` {
			t.Fatal("restored result lost its dialog source")
		}
		completed.Status, completed.Result = "inProgress", jsontext.Value("null")
		entries = nil
		restore(completed, "interrupted")
		if len(entries) != 1 || entries[0].Text != "Prepare batch · unfinished (history only)" || entries[0].native.running {
			t.Fatal("restored pending call claims live work")
		}
		completed.Tool, completed.Status = "wait_agent", "completed"
		completed.Arguments = jsontext.Value(`{"targets":["/root/batch","check"]}`)
		entries = nil
		restore(completed, "completed")
		if len(entries) != 1 || entries[0].Kind != "progress" || entries[0].Text != "Finished waiting · batch, check" {
			t.Fatal("restored wait lost its target presentation", entries)
		}
		completed.Status = "inProgress"
		entries = nil
		restore(completed, "interrupted")
		if len(entries) != 1 || entries[0].Text != "Wait unfinished (history only) · batch, check" || entries[0].native.running {
			t.Fatal("restored targeted wait revived work", entries)
		}
	}
}

func TestOrchestrateActivityOutputRetention(t *testing.T) {
	u, _ := newAppServerTestUI()
	var entry activityPaneEntry
	entry.native = &liveActivityNativeItem{}
	item := appServerItem{Type: "mcpToolCall", Server: "orchestrate", Tool: "list_agents", Status: "completed",
		Result: mustMarshalJSON(map[string]any{"content": []any{map[string]any{"type": "text", "text": strings.Repeat("line\n", 100)}}})}
	u.orchestrationActivity(&entry, item, "item/completed", true)
	block := entry.native.orchestration
	if len(block.Tail) != 0 || block.TailOmitted != 0 || len(block.Output.View().Lines) != 100 {
		t.Fatal("raw output reached the feed or dialog retention was lost")
	}
}

func TestUISnapshotOrchestrateSharedEvents(t *testing.T) {
	for _, tc := range []struct {
		name, tool, state string
		failed            bool
	}{
		{"started", "spawn_agent", "launched", false},
		{"followup", "followup_task", "delivered", false},
		{"message", "send_message", "reported", false},
		{"queued", "send_message", "queued", false},
		{"waiting", "wait_agent", "", false},
		{"wait-error", "wait_agent", "", true},
		{"tool-error", "followup_task", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.ctx = t.Context()
			u.session.start("main", t.TempDir())
			u.agents = newLiveActivityView()
			u.ensureShell()
			t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
			u.view.painter.Theme = livediff.DarkTheme
			u.clock = func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.Local) }
			u.view.clock = u.clock
			text := `{"id":"opaque-delivery-id","state":"` + tc.state + `"}`
			if tc.failed {
				text = "Target is unavailable"
			}
			item := map[string]any{"id": "call", "type": "mcpToolCall", "server": "orchestrate", "tool": tc.tool, "status": "completed",
				"arguments": map[string]any{"target": "batch", "message": "Check the rendered output."},
				"result":    map[string]any{"isError": tc.failed, "content": []any{map[string]any{"type": "text", "text": text}}}}
			if tc.name == "started" {
				item["arguments"] = map[string]any{"task_name": "batch", "message": "Check the rendered output."}
			}
			if tc.tool == "wait_agent" {
				item["arguments"] = map[string]any{"targets": []string{"batch", "/root/check"}}
			}
			if tc.name == "message" {
				owner, _ := newAppServerTestUI()
				owner.orchestrateThreads = map[string]*orchestrateChild{"main": {batch: orchestrate.Batch{TaskName: "batch"}}}
				u.navigation = &orchestrateNavigation{owner: owner, viewed: u}
				item["arguments"] = map[string]any{"target": "main", "message": "Check the rendered output."}
			}
			item["status"] = "inProgress"
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			item["status"] = "completed"
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			if len(u.view.entries) != 1 || len(u.agents.entries) != 1 {
				t.Fatal("completion duplicated or lost the call")
			}
			if tc.name == "waiting" {
				// Consecutive waits remain peer progress rows in Main.
				item["id"], item["status"], item["arguments"] = "next-wait", "inProgress", map[string]any{}
				appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			}
			if feed := strings.Join(u.agents.renderFeed(90, 60).lines, "\n"); strings.Contains(feed, "Check the rendered output.") || strings.Contains(feed, "waiting") || strings.Contains(feed, "Waiting") {
				t.Fatal("orchestration call entered native Activity")
			}
			rows, _ := u.mainFrame(70, 14, 0)
			assertNativeUISnapshot(t, "orchestration-event-"+tc.name, rows)
			uisnapshot.AssertTerminal(t, "testdata/snapshots/orchestration-event-"+tc.name+"-styles.txt", append(rows, "plain after event"), 70)
			block := u.view.entries[0].blocks[0]
			opened := false
			for _, run := range u.view.runs {
				for _, snippet := range run.snippets {
					if snippet.run != 0 {
						opened = u.shell.openOutput(u.view, snippet) || opened
					}
				}
			}
			if !opened {
				t.Fatal("rendered event has no output dialog target")
			}
			if got := u.view.painter.DialogPage(block, 70).Text; got != text {
				t.Fatalf("dialog output = %q, want %q", got, text)
			}
			if w.Len() != 0 {
				t.Fatal("presentation dispatched host work")
			}
		})
	}
}
