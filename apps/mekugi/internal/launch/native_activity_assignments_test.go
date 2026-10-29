package launch

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/charmbracelet/x/ansi"
)

func TestNativeActivityFollowupAssignments(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentRole": "review",
		"agentNickname": strings.TrimPrefix("/root/reviewer", "/root/")}})
	collab := func(id, tool, prompt string) {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": coordinationTestCall(t, id, tool, "child", u.attached.session.Caller("child"), prompt)})
	}
	collab("task-1", "spawn_agent", "Check the original answer.")
	collab("task-2", "followup_task", "Check the edited answer.")
	var next []sessionpresentation.ActivityEntry
	for _, entry := range u.attached.source.Entries {
		if entry.Source.Kind == "assignment" {
			next = append(next, entry.Source)
		}
	}
	if len(next) != 1 || next[0].Assignment.Text != "Check the edited answer." {
		t.Fatalf("follow-up was omitted or conflated with spawn: %+v", next)
	}
	applyActivity(&u.attached, &u.approvals, u.shell, u.review, &u.pendingEditReceipts, u.Thread, u.clock, []sessionpresentation.ActivityEntry{{Seq: u.attached.session.Activity().NextSequence(), Agent: "/root/reviewer", Kind: "final", Text: "The edited answer is correct."}}, nil)
	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	feed := u.testActivity(u.shell.Transcript).RenderFeed(100, 40)
	main := ansi.Strip(strings.Join(feed.Lines, "\n"))
	if !strings.Contains(main, "▶ main → reviewer started") || !strings.Contains(main, "├─✉ main → reviewer assignment") || !strings.Contains(main, "├─✓ finished") || strings.Contains(main, "↩ re:") {
		t.Fatalf("follow-up answer was not threaded under its assignment:\n%s", main)
	}
	for _, entry := range u.attached.source.Entries {
		if entry.Source.Kind == "start" && entry.Source.Agent != "Main" {
			t.Fatal("root spawn rendered as child speech")
		}
	}
}

func TestNativeActivityChildResultsStaySeparateAndLinked(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentRole": "review",
		"agentNickname": strings.TrimPrefix("/root/reviewer", "/root/")}})
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "other", "agentRole": "explorer",
		"agentNickname": strings.TrimPrefix("/root/other", "/root/")}})
	collab := func(id, tool, receiver, prompt string) {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": coordinationTestCall(t, id, tool, receiver, u.attached.session.Caller(receiver), prompt)})
	}
	result := func(agent, answer string) {
		applyActivity(&u.attached, &u.approvals, u.shell, u.review, &u.pendingEditReceipts, u.Thread, u.clock, []sessionpresentation.ActivityEntry{{Seq: u.attached.session.Activity().NextSequence(), Agent: agent, Kind: "final", Text: answer}}, nil)
	}
	collab("task-1", "spawn_agent", "child", "Review the first change.")
	result("/root/reviewer", "FIRST ANSWER")
	collab("task-2", "followup_task", "child", "Review the second change.")
	collab("task-3", "spawn_agent", "other", "Explore elsewhere.")
	result("/root/other", "OTHER ANSWER")
	result("/root/reviewer", "SECOND ANSWER")
	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	main := ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(100, 80).Lines, "\n"))
	first, second := strings.Index(main, "FIRST ANSWER"), strings.Index(main, "SECOND ANSWER")
	if first < 0 || second < first || strings.Count(main, "SECOND ANSWER") != 1 {
		t.Fatalf("consecutive child results merged:\n%s", main)
	}
	if context := strings.LastIndex(main[:second], "↩ re:"); context < 0 || !strings.Contains(main[context:second], "Review the second change.") {
		t.Fatalf("child result lost its assignment link:\n%s", main)
	}
}
