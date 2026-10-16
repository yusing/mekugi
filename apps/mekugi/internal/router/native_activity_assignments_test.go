package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestNativeActivityFollowupAssignments(t *testing.T) {
	// The native claim keeps child activity out of Main's provider responses
	// while retaining prompt bodies missing from V2 app-server notifications.
	a := newSubagentActivity()
	a.attachNativePane("main")
	a.observe("child", "main", "/root/reviewer", true)
	first := nativeAssignmentTestMessage("/root/reviewer", "NEW_TASK", "Check the original answer.")
	first["id"] = "task-1"
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"model": "gpt-6-astra", "input": []any{first}}))
	if err != nil {
		t.Fatal(err)
	}
	a.collectSubagentStart("child", &request, "/root/reviewer")
	observed := a.takeNativeActivity("main")
	if len(observed) != 1 || observed[0].assignment == nil || observed[0].assignment.text != "Check the original answer." {
		t.Fatal("native assignment body was lost")
	}

	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentRole": "review",
		"source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"agent_path": "/root/reviewer"}}}}})
	collab := func(id, tool, prompt string) {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": id, "type": "collabAgentToolCall",
			"tool": tool, "senderThreadId": "main", "receiverThreadIds": []string{"child"}, "prompt": prompt}})
	}
	collab("task-1", "spawnAgent", "Check the original answer.")
	collab("task-2", "followupTask", "Check the edited answer.")
	var next []activityPaneEntry
	for _, entry := range u.view.Entries {
		if entry.Source.Kind == "assignment" {
			next = append(next, entry.Source)
		}
	}
	if len(next) != 1 || next[0].assignment.text != "Check the edited answer." {
		t.Fatalf("follow-up was omitted or conflated with spawn: %+v", next)
	}
	u.applyActivity([]activityPaneEntry{{Seq: u.session.next(), Agent: "/root/reviewer", Kind: "final", Text: "The edited answer is correct."}}, nil)
	u.view.Conversation = true
	feed := u.view.RenderFeed(100, 40)
	main := ansi.Strip(strings.Join(feed.Lines, "\n"))
	if !strings.Contains(main, "▶ reviewer started") || !strings.Contains(main, "├─→ follow-up") || !strings.Contains(main, "├─✓ finished") || strings.Contains(main, "↩ re:") {
		t.Fatalf("follow-up answer was not threaded under its assignment:\n%s", main)
	}
	for _, entry := range u.view.Entries {
		if entry.Source.Kind == "start" && entry.Source.Agent != "Main" {
			t.Fatal("root spawn rendered as child speech")
		}
	}
}

func TestNativeActivityAssignmentsPreserveOriginalTime(t *testing.T) {
	var items []any
	for i, text := range []string{"Original assignment", "First follow-up", "Second follow-up", "Third follow-up"} {
		item := nativeAssignmentTestMessage("/root/reviewer", "NEW_TASK", text)
		item["id"] = text
		item["internal_chat_message_metadata_passthrough"] = map[string]any{"create_time": 1700000000.125 + float64(i*60)}
		items = append(items, item)
	}
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": items}))
	if err != nil {
		t.Fatal(err)
	}
	// Both initial observation and a fresh router's full-history replay must
	// display original timestamps, even when they are older than queue retention.
	for range 2 {
		a := newSubagentActivity()
		a.attachNativePane("main")
		a.observe("child", "main", "/root/reviewer", true)
		a.collectSubagentStart("child", &request, "/root/reviewer")
		entries := a.takeNativeActivity("main")
		if len(entries) != 4 {
			t.Fatalf("lost historical assignments: %+v", entries)
		}
		u, _ := newAppServerTestUI()
		u.ensureShell()
		u.view.Conversation = true
		for i := range entries {
			want := time.Unix(1700000000+int64(i*60), 125000000)
			if !entries[i].Observed.Equal(want) {
				t.Fatalf("assignment %d time = %v, want %v", i, entries[i].Observed, want)
			}
			entries[i].Seq = uint64(i + 1)
		}
		u.applyActivity(entries, nil)
		rendered := ansi.Strip(strings.Join(u.view.RenderFeed(100, 60).Lines, "\n"))
		for _, entry := range entries {
			if !strings.Contains(rendered, entry.Observed.Local().Format("15:04:05")) {
				t.Fatalf("original timestamp missing from feed: %s", rendered)
			}
		}
		a.collectSubagentStart("child", &request, "/root/reviewer")
		if got := a.takeNativeActivity("main"); len(got) != 0 {
			t.Fatalf("repeated history duplicated assignments: %+v", got)
		}
	}
}

func TestNativeActivityRetentionUsesQueueTime(t *testing.T) {
	a := newSubagentActivity()
	a.observe("child", "main", "/root/reviewer", true)
	original := time.Unix(1700000000, 0)
	a.collectEvent(activityEvent{thread: "child", source: "old-assignment", kind: "assignment", text: "Historical task", observed: original})
	if len(a.events) != 1 {
		t.Fatal("event not queued")
	}
	queued := a.events[0].queued
	a.expireLocked(queued.Add(activityEventTTL - time.Nanosecond))
	if len(a.events) != 1 || !a.events[0].observed.Equal(original) {
		t.Fatal("retention used original message time")
	}
	a.expireLocked(queued.Add(activityEventTTL))
	if len(a.events) != 0 {
		t.Fatal("queue did not expire")
	}
}

// Consecutive child results stay separate and link to their latest assignment.
func TestNativeActivityChildResultsStaySeparateAndLinked(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentRole": "review",
		"source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"agent_path": "/root/reviewer"}}}}})
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "other", "agentRole": "explorer",
		"source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"agent_path": "/root/other"}}}}})
	collab := func(id, tool, receiver, prompt string) {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{"id": id, "type": "collabAgentToolCall",
			"tool": tool, "senderThreadId": "main", "receiverThreadIds": []string{receiver}, "prompt": prompt}})
	}
	result := func(agent, answer string) {
		u.applyActivity([]activityPaneEntry{{Seq: u.session.next(), Agent: agent, Kind: "final", Text: answer}}, nil)
	}
	collab("task-1", "spawnAgent", "child", "Review the first change.")
	result("/root/reviewer", "FIRST ANSWER")
	collab("task-2", "followupTask", "child", "Review the second change.")
	collab("task-3", "spawnAgent", "other", "Explore elsewhere.")
	result("/root/other", "OTHER ANSWER")
	result("/root/reviewer", "SECOND ANSWER")
	u.view.Conversation = true
	main := ansi.Strip(strings.Join(u.view.RenderFeed(100, 80).Lines, "\n"))
	first, second := strings.Index(main, "FIRST ANSWER"), strings.Index(main, "SECOND ANSWER")
	if first < 0 || second < first || strings.Count(main, "SECOND ANSWER") != 1 {
		t.Fatalf("consecutive child results merged:\n%s", main)
	}
	if context := strings.LastIndex(main[:second], "↩ re:"); context < 0 || !strings.Contains(main[context:second], "Review the second change.") {
		t.Fatalf("child result lost its assignment link:\n%s", main)
	}
}

func nativeAssignmentTestMessage(recipient, kind, payload string) map[string]any {
	return map[string]any{
		"type": "agent_message", "author": "/root", "recipient": recipient,
		"content": []any{map[string]any{"type": "input_text",
			"text": "Message Type: " + kind + "\nTask name: " + recipient + "\nSender: /root\nPayload:\n" + payload}},
	}
}
