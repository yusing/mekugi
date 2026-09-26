package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestNativeActivityFollowupAssignments(t *testing.T) {
	// The native claim keeps child activity out of Main's provider responses
	// without queueing it; app-server supplies the displayed activity.
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
	if len(a.events) != 0 || len(a.drain("main", time.Time{}, maxCommentaryPublicationBytes)) != 0 {
		t.Fatal("native-claimed activity was queued for inline delivery")
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
			next = append(next, entry)
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
	if !strings.Contains(main, "▶ reviewer started") || !strings.Contains(main, "↩ re: assignment") || strings.Contains(main, "not loaded") {
		t.Fatalf("follow-up answer target missing:\n%s", main)
	}
	var target uint64
	for _, entry := range u.view.entries {
		if entry.Kind == "assignment" && entry.assignment.text == "Check the edited answer." {
			target = entry.Seq
		}
	}
	linked := false
	for _, link := range feed.questions {
		linked = linked || link == target && target != 0
	}
	if !linked {
		t.Fatal("answer did not link to its follow-up assignment")
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
	groups := u.view.blocks[len(u.view.blocks)-1][0].journal.groups
	if len(groups) != 2 || groups[0].target != 1 || groups[1].target != 3 {
		t.Fatalf("cumulative answers lost their original assignment targets: %+v", groups)
	}
	feed := u.view.renderFeed(100, 60)
	if strings.Contains(ansi.Strip(strings.Join(feed.lines, "\n")), "not loaded") {
		t.Fatal("normalized question text did not resolve to the original assignment")
	}
	for _, target := range []uint64{1, 3} {
		found := false
		for _, link := range feed.questions {
			found = found || link == target
		}
		if !found {
			t.Fatalf("rendered answer has no clickable target %d", target)
		}
	}
}
