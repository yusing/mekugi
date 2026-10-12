package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
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
		if len(entries) != 1 || entries[0].Text != "prepare batch · call unfinished (history only)" || entries[0].native.running {
			t.Fatal("restored pending call claims live work")
		}
	}
}

func TestOrchestrateActivityOutputRetention(t *testing.T) {
	u, _ := newAppServerTestUI()
	var entry activityPaneEntry
	entry.native = &liveActivityNativeItem{}
	item := appServerItem{Type: "mcpToolCall", Server: "orchestrate", Tool: "list_agents", Status: "completed",
		Result: mustMarshalJSON(map[string]any{"content": []any{map[string]any{"type": "text", "text": strings.Repeat("line\n", 100)}}})}
	u.session.orchestrationActivity(&entry, item, "item/completed", true)
	block := entry.native.orchestration
	if len(block.Tail) != 5 || block.TailOmitted != 95 || len(block.Output.View().Lines) != 100 {
		t.Fatal("display tail or dialog retention is wrong")
	}
	// Mutating the owner's backing slice must not alter the copied preview.
	block.Output.View().Lines[99] = "changed"
	if block.Tail[4] != "line" {
		t.Fatal("preview retains the output owner's backing slice")
	}
}
