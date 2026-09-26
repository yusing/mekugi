package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestNativeActivityFollowupAssignments(t *testing.T) {
	a := newSubagentActivity()
	generation, _ := a.attachNativePane("main")
	a.observe("child", "main", "/root/reviewer", true)
	u, _ := newAppServerTestUI()
	u.ensureShell()
	first := journalTestAssignment("/root/reviewer", "NEW_TASK", "Check the original answer.")
	first["id"] = "task-1"
	followup := journalTestAssignment("/root/reviewer", "NEW_TASK", "Check the edited answer.")
	followup["id"] = "task-2"
	collect := func(items ...any) []activityPaneEntry {
		t.Helper()
		request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"model": "gpt-6-astra", "input": items}))
		if err != nil {
			t.Fatal(err)
		}
		a.collectSubagentStart("child", &request, "/root/reviewer")
		entries, agents, ok := a.takePane(generation)
		if !ok {
			t.Fatal("native collector detached")
		}
		u.applyActivity(entries, agents)
		return entries
	}
	initial := collect(first)
	if len(initial) != 1 || initial[0].Kind != "start" || initial[0].assignment.text != "Check the original answer." {
		t.Fatalf("missing initial native assignment: %+v", initial)
	}
	next := collect(first, followup)
	if len(next) != 1 || next[0].Kind != "assignment" || next[0].assignment.text != "Check the edited answer." || next[0].assignment.id == initial[0].assignment.id {
		t.Fatalf("follow-up was omitted or conflated with spawn: %+v", next)
	}
	if replay := collect(first, followup); len(replay) != 0 {
		t.Fatal("full-history replay duplicated assignments")
	}
	var child strings.Builder
	child.WriteString("Journal result `/root/reviewer`")
	writeJournalItems(&child, []journalItem{{ID: "amber", Question: "Check the edited answer.", Text: "The edited answer is correct."}})
	u.applyActivity([]activityPaneEntry{{Seq: next[0].Seq + 1, Agent: "/root/reviewer", Kind: "final", Text: child.String()}}, nil)
	u.view.conversation = true
	feed := u.view.renderFeed(100, 40)
	main := ansi.Strip(strings.Join(feed.lines, "\n"))
	if !strings.Contains(main, "▶ reviewer started") || !strings.Contains(main, "↩ reply to assignment") || strings.Contains(main, "not loaded") {
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
