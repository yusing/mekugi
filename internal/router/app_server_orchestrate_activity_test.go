package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
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
		if len(entries) != 1 || entries[0].Kind != "progress" || entries[0].Text != "Wait ended (no event reported)" {
			t.Fatal("restored wait invented a result", entries)
		}
		completed.Status = "inProgress"
		entries = nil
		restore(completed, "interrupted")
		if len(entries) != 1 || entries[0].Text != "Wait unfinished (history only)" || entries[0].native.running || parseLiveActivity(entries[0])[0].Live {
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

func TestOrchestrateWaitEventLiveAndHistory(t *testing.T) {
	// The same returned event is available as MCP structured content or text.
	// Neither the seven selected targets nor the message body is the wake source.
	for _, tc := range []struct{ kind, status, reason string }{
		{"message", "", "Message"},
		{"failure", "canceled", "Recorded failure (canceled)"},
	} {
		result := fmt.Sprintf(`{"id":"wait-id","state":"completed","event_id":"old-event","event":{"task_name":"archive_integrated","thread_id":"child","kind":%q,"status":%q,"message":"private review payload"}}`, tc.kind, tc.status)
		for _, structured := range []bool{false, true} {
			u, wire := newAppServerTestUI()
			u.session.start("main", t.TempDir())
			u.agents = newLiveActivityView()
			item := appServerItem{ID: "wait-id", Type: "mcpToolCall", Server: "orchestrate", Tool: "wait_agent", Status: "inProgress",
				Arguments: jsontext.Value(`{"targets":["batch","archive_integrated","check","review","docs","tests","cleanup"]}`)}
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			if len(u.view.entries[0].native.orchestration.WaitTargets) != 7 {
				t.Fatal("active wait lost selected targets")
			}
			content := map[string]any{"content": []any{map[string]any{"type": "text", "text": result}}}
			if structured {
				content["structuredContent"] = jsontext.Value(result)
			}
			item.Status, item.Result = "completed", mustMarshalJSON(content)
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			want := "Wait yielded · archive_integrated: " + tc.reason
			if len(u.view.entries) != 1 || u.view.entries[0].Text != want || u.view.entries[0].native.item != "wait-id" {
				t.Fatalf("wait result: got %q, want %q", u.view.entries[0].Text, want)
			}
			for _, agent := range []string{"Main", "/root/batch"} {
				var entries []activityPaneEntry
				if agent == "Main" {
					u.view = newLiveActivityView()
					u.restoreHistoryItem(appServerHistoryTurn{ID: "turn", Status: "completed"}, item)
					entries = append(entries, u.view.entries[0].activityPaneEntry)
				} else {
					last := -1
					u.restoreActivityItem(appServerThreadInfo{ID: "child"}, appServerHistoryTurn{ID: "turn", Status: "completed"}, item, agent, time.Time{}, &entries, &last)
				}
				if len(entries) != 1 || entries[0].Text != want || entries[0].native.running {
					t.Fatal("history lost the event or revived work", entries)
				}
				block := parseLiveActivity(entries[0])[0]
				if got := block.Output.View().Lines; len(got) != 1 || got[0] != result {
					t.Fatal("history changed result identity or payload", got)
				}
			}
			if wire.Len() != 0 {
				t.Fatal("presentation dispatched host work")
			}
		}
	}
}

func TestUISnapshotOrchestrateSharedEvents(t *testing.T) {
	for _, tc := range []struct {
		name, tool, state string
		failed            bool
	}{
		{"started", "spawn_agent", "prepared", false},
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
			if tc.tool == "spawn_agent" {
				text = `{"state":"prepared","launch":{"thread_id":"child","turn_id":"turn"}}`
			}
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

func TestUISnapshotOrchestrateWaitBreathing(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	v := u.navigation.views["child"]
	now := time.Unix(0, 0)
	u.clock = func() time.Time { return now }
	u.view.clock, u.agents.clock = u.clock, u.clock
	u.view.painter.Theme = livediff.DarkTheme
	item := appServerItem{ID: "wait", Type: "mcpToolCall", Server: "orchestrate", Tool: "wait_agent", Status: "inProgress", Arguments: jsontext.Value(`{"targets":["batch","/root/check"],"timeout_ms":12500}`)}
	var rows []string
	for _, state := range []string{"live", "completed", "failed", "interrupted"} {
		item.ID, item.Status = state, "inProgress"
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": state}})
		appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": state, "item": item})
		switch state {
		case "completed", "failed":
			item.Status = state
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": state, "item": item})
		case "interrupted":
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": state, "status": state}})
		}
		v.orchestrationRoster()
		if u.view.hasLiveStatus() != (state == "live") || v.agents.hasLiveStatus() != (state == "live") {
			t.Fatalf("%s: transcript or other thread roster animation scheduling", state)
		}
		rows = append(rows, state)
		for _, phase := range []time.Duration{0, time.Second, 2 * time.Second} {
			now = time.Unix(0, 0).Add(phase)
			frame := u.view.renderFeed(80, 100).lines
			// Only retain this state's row; preceding completed waits stay visible.
			got := frame[len(frame)-1]
			rows = append(rows, got, "plain after wait")
		}
		if state == "live" { // Settle this row before the next independent lifecycle.
			item.Status = "completed"
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": state, "item": item})
		}
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/orchestration-wait-breathing.txt", rows, 80)
}

func TestUISnapshotOrchestrateLogicalWait(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ctx = t.Context()
	u.session.start("main", t.TempDir())
	u.agents = newLiveActivityView()
	u.clock = func() time.Time { return time.Unix(0, 0) }
	u.view.clock = u.clock
	u.view.painter.Theme = livediff.DarkTheme
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	for _, tc := range []struct {
		name, result string
	}{
		{"registered", `{"state":"registered"}`},
		{"message", `{"state":"completed","event":{"task_name":"archive_integrated","kind":"message","message":"private payload"}}`},
		{"done", `{"state":"completed","event":{"task_name":"batch","kind":"done","status":"completed"}}`},
		{"failure", `{"state":"completed","event":{"task_name":"batch","kind":"failure","status":"storage_failed"}}`},
		{"historical-cancellation", `{"state":"completed","event":{"task_name":"batch","kind":"failure","status":"canceled"}}`},
		{"blocked", `{"state":"completed","event":{"task_name":"batch","kind":"blocked"}}`},
		{"timeout", `{"state":"completed","timed_out":true}`},
		{"canceled", `{"state":"canceled"}`},
		{"uncertain", `{"state":"uncertain","event":{"task_name":"batch","kind":"message"}}`},
		{"delivered", `{"state":"delivered"}`},
		{"no-event", `{"state":"completed"}`},
	} {
		args := map[string]any{"targets": []string{"batch", "archive_integrated", "check", "review", "docs", "tests", "cleanup"}}
		if tc.name == "canceled" {
			args = map[string]any{"cancel_id": "wait-id"}
		}
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{
			"id": tc.name, "type": "mcpToolCall", "server": "orchestrate", "tool": "wait_agent", "status": "completed", "arguments": args,
			"result": map[string]any{"structuredContent": jsontext.Value(tc.result)},
		}})
	}
	rows, _ := u.mainFrame(100, 27, 0)
	assertNativeUISnapshot(t, "orchestration-logical-wait", rows)
	uisnapshot.AssertTerminal(t, "testdata/snapshots/orchestration-logical-wait-styles.txt", append(rows, "plain after wait"), 100)
}

func TestUISnapshotOrchestrateDelayedWait(t *testing.T) {
	var rows []string
	cases := []struct{ name, want string }{
		{"message", "Wait yielded · batch: Message"},
		{"failure", "Wait yielded · batch: Recorded failure (storage failed)"},
		{"historical-cancellation", "Wait yielded · batch: Recorded failure (canceled)"},
		{"timeout", "Wait timed out"},
		{"rejected", "Wait delivery failed · batch: Message"},
		{"canceled", "Wait canceled"},
	}
	ran := 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ran++
			u, launch := orchestrateIdentityPendingTurn(t)
			orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
			orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"child-turn"}}}`)
			store, workspace := u.proxy.orchestration.store, u.session.cwd
			now := time.Unix(0, 0)
			u.clock, u.view.clock = func() time.Time { return now }, func() time.Time { return now }
			u.view.painter.Theme = livediff.DarkTheme
			targets := []string{"batch"}
			if tc.name == "message" {
				for _, name := range []string{"check", "review", "docs", "tests", "cleanup", "other"} {
					if _, err := store.Prepare(t.Context(), workspace, "main", name); err != nil {
						t.Fatal(err)
					}
					targets = append(targets, name)
				}
			}
			command := &orchestrateCommand{ctx: t.Context(), workspace: workspace, main: "main", tool: "wait_agent", callID: "logical-wait", waitTargets: targets, reply: make(chan orchestrateResult, 1)}
			if tc.name == "timeout" {
				command.waitTimeout = 1
			}
			u.startOrchestratedChild(command)
			drainOrchestrateWork(t, u)
			registration := <-command.reply
			if registration.err != nil || registration.wait.State != "registered" {
				t.Fatal("wait did not register", registration)
			}
			item := appServerItem{ID: "mcp-item", Type: "mcpToolCall", Server: "orchestrate", Tool: "wait_agent", Status: "completed",
				Arguments: mustMarshalJSON(map[string]any{"targets": targets}), Result: mustMarshalJSON(map[string]any{
					"structuredContent": registration.wait,
					"content":           []any{map[string]any{"type": "text", "text": string(mustMarshalJSON(registration.wait))}},
				})}
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			if tc.name == "message" {
				rows = append(rows, u.view.painter.Block(u.view.entries[0].blocks[0], 100)...)
			}
			// The coordinator still owns the wait while another root is viewed.
			u.navigation.viewed = u.navigation.views["child"]
			wire := u.client.Input.(*appServerTestInput)
			switch tc.name {
			case "timeout":
				now = now.Add(time.Millisecond)
				u.advanceOrchestratedWaits(workspace, "main", nil)
			case "canceled":
				cancel := &orchestrateCommand{ctx: t.Context(), workspace: workspace, main: "main", tool: "wait_agent", callID: "cancel", waitCancel: command.callID, reply: make(chan orchestrateResult, 1)}
				u.startOrchestratedChild(cancel)
			default:
				event := orchestrateEvent{ID: "wake-event", Task: "batch", Thread: "child", Kind: "message", Message: "private event payload"}
				if tc.name == "failure" {
					event.Kind, event.Status = "failure", "storage_failed"
				}
				if tc.name == "historical-cancellation" {
					event.Kind, event.Status, event.Turn = "failure", "canceled", "earlier-turn"
				}
				u.publishOrchestratedEvent(u.orchestrateThreads["child"], event)
			}
			drainOrchestrateWork(t, u)
			if tc.name != "canceled" {
				if !strings.HasPrefix(u.view.entries[0].Text, "Wait registered") {
					t.Fatal("unacknowledged delivery claimed completion")
				}
				inject := btwTestRequest(t, wire, "thread/inject_items", "main")
				if tc.name == "rejected" {
					orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"message":"delivery rejected"}}`, inject.ID))
				} else {
					orchestrateTestReply(t, u, inject, `{}`)
				}
			}
			items := []appServerItem{item}
			assertResult := func(view *appServerUI) {
				t.Helper()
				if len(view.view.entries) != len(items) {
					t.Fatal("delayed result duplicated or lost the wait row", len(view.view.entries))
				}
				if row := view.view.entries[0]; row.Text != tc.want || row.native.item != item.ID || row.native.running {
					t.Fatalf("delayed result row: %q, item=%q, running=%t", row.Text, row.native.item, row.native.running)
				}
				var result orchestrate.Wait
				if err := json.Unmarshal([]byte(strings.Join(view.view.entries[0].blocks[0].Output.View().Lines, "\n")), &result); err != nil || result.ID != command.callID {
					t.Fatal("result dialog lost wait identity", result, err)
				}
				if tc.name != "timeout" && tc.name != "canceled" && (result.EventID != "wake-event" || !strings.Contains(string(result.Event), "private event payload")) {
					t.Fatal("result dialog lost event identity or payload", result)
				}
				if len(items) == 2 {
					row := view.view.entries[1]
					if row.Text != "Wait result delivered" || row.native.item != items[1].ID {
						t.Fatalf("delivery acknowledgement row: %q, item=%q", row.Text, row.native.item)
					}
					if got := strings.Join(row.blocks[0].Output.View().Lines, "\n"); got != string(mustMarshalJSON(registration.wait)) {
						t.Fatalf("delivery acknowledgement changed host output: %s", got)
					}
				}
			}
			assertResult(u)
			if child := u.orchestrateThreads["child"]; child.turn != "child-turn" || child.batch.Launch.HostStatus != "running" || wire.Len() != 0 {
				t.Fatal("wait delivery changed or interrupted current child work", child, wire.String())
			}
			rows = append(rows, u.view.painter.Block(u.view.entries[0].blocks[0], 100)...)
			if tc.name == "message" {
				u.startOrchestratedChild(command)
				drainOrchestrateWork(t, u)
				registration = <-command.reply
				if registration.err != nil || registration.wait.State != "delivered" || len(registration.wait.Event) != 0 {
					t.Fatal("repeated call did not acknowledge delivery", registration)
				}
				repeated := item
				repeated.ID = "mcp-repeat"
				repeated.Result = mustMarshalJSON(map[string]any{
					"structuredContent": registration.wait,
					"content":           []any{map[string]any{"type": "text", "text": string(mustMarshalJSON(registration.wait))}},
				})
				items = append(items, repeated)
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": repeated})
				assertResult(u)
				rows = append(rows, u.view.painter.Block(u.view.entries[1].blocks[0], 100)...)
			}
			orders := []bool{tc.name == "timeout"}
			if tc.name == "message" {
				orders = []bool{false, true}
			}
			for _, recoveryFirst := range orders {
				resumed, resumedWire := resumeOrchestrationMain(t, store, workspace)
				resumedWire.Reset()
				if recoveryFirst { // Recovery can finish before history arrives.
					drainOrchestrateWork(t, resumed)
				}
				for _, recorded := range items {
					resumed.restoreHistoryItem(appServerHistoryTurn{ID: "turn", Status: "completed"}, recorded)
				}
				drainOrchestrateWork(t, resumed)
				assertResult(resumed)
				if resumedWire.Len() != 0 {
					t.Fatal("restoration dispatched host work", resumedWire.String())
				}
			}
		})
	}
	if t.Failed() || ran != len(cases) { // A combined snapshot requires all cases.
		return
	}
	uisnapshot.Assert(t, "testdata/snapshots/orchestration-delayed-wait.txt", strings.Join(rows, "\n"))
	uisnapshot.AssertTerminal(t, "testdata/snapshots/orchestration-delayed-wait-styles.txt", append(rows, "plain after wait"), 100)
}
