package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestAppServerProgressCompactionLifecycle(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": "q", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "Do the work"}}}})
	item := func(thread, turn, id, method string) {
		t.Helper()
		appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": turn, "item": map[string]any{"id": id, "type": "contextCompaction"}})
	}
	label := func(want string) {
		t.Helper()
		frame, _ := u.mainFrame(80, 18, 0)
		if got := ansi.Strip(frame[u.composerRect.y-1]); !strings.Contains(got, want) {
			t.Fatalf("composer = %q, want %q", got, want)
		}
	}
	item("main", "t", "c", "item/started")
	label("Compacting context")
	item("child", "t", "c", "item/completed")
	item("main", "old", "c", "item/completed")
	item("main", "t", "other", "item/completed")
	label("Compacting context")
	item("main", "t", "c", "item/completed")
	label("Working")
	before := len(u.view.Entries)
	item("main", "t", "c", "item/completed")
	item("main", "t", "c", "item/started")
	label("Working")
	if len(u.view.Entries) != before {
		t.Fatal("duplicate compaction completion created an event")
	}
	frame, _ := u.mainFrame(80, 24, 0)
	screen := ansi.Strip(strings.Join(frame, "\n"))
	if !strings.Contains(screen, "Context compacted") || strings.Contains(screen, "re:") {
		t.Fatalf("compaction should be an event without reply context: %s", screen)
	}
	for _, status := range []string{"interrupted", "failed", "completed"} {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": status}})
		item("main", status, "pending", "item/started")
		before = len(u.view.Entries)
		appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": status, "status": status}})
		if u.compacting != nil || strings.Contains(u.sessionLabel(time.Now()), "Compacting") || len(u.view.Entries) != before {
			t.Fatal("turn end retained compaction or fabricated completion")
		}
	}
}

func TestAppServerProgressWaitAndReplay(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	wait := appServerItem{ID: "w", Type: "collabAgentToolCall", Tool: "wait", Status: "inProgress", ReceiverThreadIDs: []string{"child"}}
	for _, method := range []string{"item/started", "item/completed"} {
		if method == "item/completed" {
			wait.Status = "completed"
			wait.AgentsStates = map[string]appServerAgentState{"child": {Status: "running"}}
		}
		appServerTestNotify(t, u, method, map[string]any{"threadId": "main", "turnId": "t", "item": wait})
		if got := ansi.Strip(u.sessionLabel(time.Now())); !strings.Contains(got, "Working") {
			t.Fatalf("wait replaced composer: %q", got)
		}
		if len(u.view.Entries) != 1 || u.view.Entries[0].Source.Kind != "progress" || u.view.Entries[0].Source.native.question != 0 {
			t.Fatalf("wait did not reconcile as an event: %+v", u.view.Entries)
		}
	}
	if !strings.Contains(u.view.Entries[0].Source.Text, "Finished waiting") || !strings.Contains(u.view.Entries[0].Source.Text, "child: Still running") {
		t.Fatal("wait start did not complete")
	}
	history := []appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{wait, {ID: "c", Type: "contextCompaction"}}}}
	v := newAppServerSessionTestUI(t, t.TempDir())
	v.restoreHistory(history)
	if len(v.view.Entries) != 2 || v.compacting != nil || v.turn != "" {
		t.Fatalf("restored events revived work or went missing: %+v", v.view.Entries)
	}
	v.session.registerThread(appServerThreadInfo{ID: "child"})
	v.restoreActivityThread(appServerThreadInfo{ID: "child", Turns: history})
	if len(v.agents.Entries) != 2 || v.agents.Entries[1].Source.Text != "Context compacted" {
		t.Fatalf("child replay missing progress: %+v", v.agents.Entries)
	}
}

func TestAppServerProgressUnfinishedChildWait(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	wait := appServerItem{ID: "w", Type: "collabAgentToolCall", Tool: "wait", Status: "inProgress"}
	u.session.registerThread(appServerThreadInfo{ID: "child"})
	u.restoreActivityThread(appServerThreadInfo{ID: "child", Turns: []appServerHistoryTurn{{ID: "t", Status: "inProgress", Items: []appServerItem{wait}}}})
	if !strings.Contains(u.agents.Entries[0].Source.Text, "Waiting for agent") || u.session.agent(u.session.paths["child"]).Responding {
		t.Fatal("unfinished historical wait claimed completion or revived work")
	}
	// A later turn must not restart animation for the restored wait.
	agent := *u.session.agent(u.session.paths["child"])
	agent.Responding = true
	first, _ := u.agents.current(agent, time.Unix(0, 0))
	second, _ := u.agents.current(agent, time.Unix(1, 0))
	if first != second {
		t.Fatal("restored wait animated in a later turn")
	}
	wait.Status = "failed"
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "t", "item": wait})
	if u.agents.Entries[0].Source.Text != "Wait failed" || u.agents.Entries[0].Source.native.phase != "item/completed" {
		t.Fatalf("live completion could not replace history: %+v", u.agents.Entries[0])
	}
}

func TestAppServerWaitRosterOnly(t *testing.T) {
	for _, caller := range []string{"main", "child"} {
		t.Run(caller, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.session.registerThread(appServerThreadInfo{ID: "child"})
			feed := u.view
			name := "/root"
			if caller == "child" {
				feed, name = u.agents, u.session.path(caller)
			}
			check := func(want string) {
				t.Helper()
				if rows := feed.renderFeed(100, 30).lines; len(rows) != 0 {
					t.Fatalf("wait leaked into transcript: %q", rows)
				}
				if got, _ := u.agents.current(activityPaneAgent{Name: name}, time.Now()); !strings.Contains(ansi.Strip(got), want) {
					t.Fatalf("roster = %q, want %q", got, want)
				}
			}
			var items []appServerItem
			for _, id := range []string{"w1", "w2", "w3"} {
				wait := appServerItem{ID: id, Type: "collabAgentToolCall", Tool: "wait", Status: "inProgress", ReceiverThreadIDs: []string{"target"}}
				notify := func(method string) {
					appServerTestNotify(t, u, method, map[string]any{"threadId": caller, "turnId": "t", "item": wait})
				}
				notify("item/started")
				check("Waiting for agent · target")
				wait.Status = "completed"
				want := "Finished waiting · target: Still running"
				wait.AgentsStates = map[string]appServerAgentState{"target": {Status: "running"}}
				if id == "w3" {
					wait.Status, want = "failed", "Wait failed · target: Still running"
				}
				notify("item/completed")
				notify("item/completed")
				check(want)
				items = append(items, wait)
				if len(feed.Entries) != len(items) {
					t.Fatalf("same-item duplicate not reconciled: %d entries", len(feed.Entries))
				}
			}
			// Full history and repeated live delivery must preserve roster-only
			// presentation without resurrecting work or creating feed rows.
			history := []appServerHistoryTurn{{ID: "t", Status: "completed", Items: items}}
			if caller == "main" {
				u.restoreHistory(history)
			} else {
				u.restoreActivityThread(appServerThreadInfo{ID: caller, Turns: history})
			}
			check("Wait failed · target: Still running")
		})
	}
}

func TestAppServerProgressTerminalPolling(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	poll := func(thread, turn, process, stdin string) {
		t.Helper()
		appServerTestNotify(t, u, "item/commandExecution/terminalInteraction", map[string]any{"threadId": thread, "turnId": turn, "itemId": "cmd", "processId": process, "stdin": stdin})
	}
	label := func(want string) {
		t.Helper()
		frame, _ := u.mainFrame(60, 12, 0)
		if got := ansi.Strip(frame[u.composerRect.y-1]); !strings.Contains(got, want) {
			t.Fatalf("composer = %q, want %q", got, want)
		}
	}
	poll("child", "t", "proc", "")
	poll("main", "old", "proc", "")
	poll("main", "t", "proc", "input")
	label("Working")
	poll("main", "t", "proc", "")
	poll("main", "t", "proc", "")
	label("Still running")
	if len(u.view.Entries) != 0 {
		t.Fatal("polls flooded transcript")
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": appServerItem{ID: "other", Type: "commandExecution", ProcessID: "other"}})
	label("Still running")
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": appServerItem{ID: "cmd", Type: "commandExecution", ProcessID: "proc"}})
	label("Working")
	poll("main", "t", "proc", "")
	poll("main", "t", "proc", "input")
	label("Working")
	poll("main", "t", "proc", "")
	appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": "main", "turnId": "t", "itemId": "a", "delta": "Continuing"})
	label("Working")
	poll("main", "t", "proc", "")
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": appServerItem{ID: "compact", Type: "contextCompaction"}})
	poll("main", "t", "proc", "")
	label("Compacting context")
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t", "status": "interrupted"}})
	if u.polling != nil || u.compacting != nil {
		t.Fatal("interrupt retained progress")
	}
}
