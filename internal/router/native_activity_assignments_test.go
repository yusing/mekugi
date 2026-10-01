package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestNativeActivityFollowupAssignments(t *testing.T) {
	// The native claim keeps child activity out of Main's provider responses
	// while retaining prompt bodies missing from V2 app-server notifications.
	a := newSubagentActivity()
	a.attachNativePane("main")
	a.observe("child", "main", "/root/reviewer", true)
	first := journalTestAssignment("/root/reviewer", "NEW_TASK", "Check the original answer.")
	first["id"] = "task-1"
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"model": "gpt-6-astra", "input": []any{first}}))
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
	for _, entry := range u.view.entries {
		if entry.Kind == "assignment" {
			next = append(next, entry.activityPaneEntry)
		}
	}
	if len(next) != 1 || next[0].assignment.text != "Check the edited answer." {
		t.Fatalf("follow-up was omitted or conflated with spawn: %+v", next)
	}
	var child strings.Builder
	child.WriteString("Journal result `/root/reviewer`")
	writeJournalItems(&child, []journalItem{{ID: "amber", Question: "Check the edited answer.", Text: "The edited answer is correct."}})
	u.applyActivity([]activityPaneEntry{{Seq: u.session.next(), Agent: "/root/reviewer", Kind: "final", Text: child.String()}}, nil)
	u.view.conversation = true
	feed := u.view.renderFeed(100, 40)
	main := ansi.Strip(strings.Join(feed.lines, "\n"))
	if !strings.Contains(main, "▶ reviewer started") || !strings.Contains(main, "├─→ follow-up") || !strings.Contains(main, "├─✓ finished") || strings.Contains(main, "↩ re:") {
		t.Fatalf("follow-up answer was not threaded under its assignment:\n%s", main)
	}
	var target uint64
	for _, entry := range u.view.entries {
		if entry.Kind == "assignment" && entry.assignment.text == "Check the edited answer." {
			target = entry.Seq
		}
	}
	if groups := u.view.entries[len(u.view.entries)-1].blocks[0].Journal.Groups; target == 0 || len(groups) == 0 || groups[len(groups)-1].Target != target {
		t.Fatalf("answer did not link to its follow-up assignment: %+v", groups)
	}
	for _, entry := range u.view.entries {
		if entry.Kind == "start" && entry.Agent != "Main" {
			t.Fatal("root spawn rendered as child speech")
		}
	}
}

func TestNativeActivityCumulativeAnswerTargets(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	u.view.conversation = true
	question := "\nReview the answer.\r\nKeep the layout.\n"
	assignment := func(seq uint64, id string) {
		u.applyActivity([]activityPaneEntry{{Seq: seq, Agent: "/root/reviewer", Kind: "assignment", assignment: &activityAssignment{id: id, from: "/root", to: "/root/reviewer", text: question}}}, nil)
	}
	completion := func(seq uint64, items ...journalItem) {
		var body strings.Builder
		body.WriteString("Journal result `/root/reviewer`")
		writeJournalItems(&body, items)
		u.applyActivity([]activityPaneEntry{{Seq: seq, Agent: "/root/reviewer", Kind: "final", Text: body.String()}}, nil)
	}
	first := journalItem{ID: "amber", Question: question, Text: "First review."}
	assignment(1, "first")
	completion(2, first)
	assignment(3, "followup") // Identical wording, distinct actual question.
	completion(4, first, journalItem{ID: "apple", Question: question, Text: "Follow-up review."})
	groups := u.view.entries[len(u.view.entries)-1].blocks[0].Journal.Groups
	if len(groups) != 1 || groups[0].Target != 3 || u.view.entries[1].blocks[0].Journal.Groups[0].Target != 1 {
		t.Fatalf("cumulative answers lost their original assignment targets: %+v", groups)
	}
	feed := u.view.renderFeed(100, 60)
	if strings.Count(ansi.Strip(strings.Join(feed.lines, "\n")), "First review.") != 1 {
		t.Fatal("cumulative snapshot repeated the previous reply")
	}
	// Each answer follows its own assignment in one thread, so neither is
	// quoted again; the targets above keep them linked.
	if main := ansi.Strip(strings.Join(feed.lines, "\n")); strings.Contains(main, "not loaded") || strings.Contains(main, "↩ re:") {
		t.Fatalf("threaded answers quoted their assignments:\n%s", main)
	}
	// Narrow Activity must use the same parsed, deduplicated answers, not
	// fall back to the original cumulative wire body.
	u.agents.only, u.agents.selected = true, "/root/reviewer"
	for _, width := range []int{13, 80} {
		text := ansi.Strip(strings.Join(u.agents.renderFeed(width, 100).lines, "\n"))
		words := strings.Join(strings.Fields(strings.NewReplacer("│", "", "▎", "").Replace(text)), " ")
		if strings.Count(words, "First review.") != 1 || strings.Contains(words, "Journal result") {
			t.Fatalf("width %d bypassed parsed journal: %s", width, text)
		}
	}
}

func TestNativeActivityAssignmentsPreserveOriginalTime(t *testing.T) {
	var items []any
	for i, text := range []string{"Original assignment", "First follow-up", "Second follow-up", "Third follow-up"} {
		item := journalTestAssignment("/root/reviewer", "NEW_TASK", text)
		item["id"] = text
		item["internal_chat_message_metadata_passthrough"] = map[string]any{"create_time": 1700000000.125 + float64(i*60)}
		items = append(items, item)
	}
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"input": items}))
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
		u.view.conversation = true
		for i := range entries {
			want := time.Unix(1700000000+int64(i*60), 125000000)
			if !entries[i].Observed.Equal(want) {
				t.Fatalf("assignment %d time = %v, want %v", i, entries[i].Observed, want)
			}
			entries[i].Seq = uint64(i + 1)
		}
		u.applyActivity(entries, nil)
		rendered := ansi.Strip(strings.Join(u.view.renderFeed(100, 60).lines, "\n"))
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
	a.expireLocked(queued.Add(commentaryRouteTTL - time.Nanosecond))
	if len(a.events) != 1 || !a.events[0].observed.Equal(original) {
		t.Fatal("retention used original message time")
	}
	a.expireLocked(queued.Add(commentaryRouteTTL))
	if len(a.events) != 0 {
		t.Fatal("queue did not expire")
	}
}

// Tree results are deltas without item IDs: consecutive results from one
// agent stay separate, and each links to the task it answers.
func TestNativeActivityTreeResultsStaySeparateAndLinked(t *testing.T) {
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
		u.applyActivity([]activityPaneEntry{{Seq: u.session.next(), Agent: agent, Kind: "final", Text: answer + "\n\n**Changes:**\nNo recorded changes.\n"}}, nil)
	}
	collab("task-1", "spawnAgent", "child", "Review the first change.")
	result("/root/reviewer", "FIRST ANSWER")
	collab("task-2", "followupTask", "child", "Review the second change.")
	collab("task-3", "spawnAgent", "other", "Explore elsewhere.")
	result("/root/other", "OTHER ANSWER")
	result("/root/reviewer", "SECOND ANSWER")
	u.view.conversation = true
	main := ansi.Strip(strings.Join(u.view.renderFeed(100, 80).lines, "\n"))
	first, second := strings.Index(main, "FIRST ANSWER"), strings.Index(main, "SECOND ANSWER")
	if first < 0 || second < first || strings.Count(main, "SECOND ANSWER") != 1 {
		t.Fatalf("consecutive tree results merged:\n%s", main)
	}
	if context := strings.LastIndex(main[:second], "↩ re:"); context < 0 || !strings.Contains(main[context:second], "Review the second change.") {
		t.Fatalf("tree result lost its assignment link:\n%s", main)
	}
}
