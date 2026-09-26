package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestLiveActivityNativeItemsShareStateAndRendering(t *testing.T) {
	v := newLiveActivityView()
	v.applyAppServerItem("root", "root", "turn", "answer", "item/agentMessage/delta", "partial", appServerItem{})
	first := v.entries[0].Seq
	v.applyAppServerItem("root", "root", "turn", "answer", "item/started", "", appServerItem{Type: "agentMessage"})
	if v.entries[0].Text != "partial" {
		t.Fatal("late start erased streamed activity")
	}
	v.applyAppServerItem("root", "root", "turn", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: "complete"})
	v.applyAppServerItem("root", "root", "turn", "answer", "item/agentMessage/delta", "late", appServerItem{})
	if len(v.entries) != 1 || v.entries[0].Text != "complete" || v.entries[0].Seq != first || v.blocks[0][0].body != "complete" {
		t.Fatal("activity identity/update diverged")
	}
	v.applyAppServerItem("root", "child", "turn", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: "child"})
	v.applyAppServerItem("root", "root", "turn", "cmd", "item/completed", "", appServerItem{Type: "commandExecution", Command: "printf ok", Status: "completed"})
	if len(v.entries) != 3 || v.entries[1].Agent != "Thread child" || v.blocks[2][0].verb != "Run" || v.blocks[2][0].code != "printf ok" {
		t.Fatal("native items did not enter shared activity blocks")
	}
	// Main is the same activity viewport, not another row cache or renderer.
	u, _ := newAppServerTestUI()
	u.view = v
	var frame bytes.Buffer
	if err := u.paint(&frame, 100, 25); err != nil {
		t.Fatal(err)
	}
	plain := ansi.Strip(frame.String())
	if !strings.Contains(plain, "┃ complete") || !strings.Contains(plain, "printf ok") || !strings.Contains(plain, "complete") {
		t.Fatalf("Main did not render activity state: %s", plain)
	}
}

func TestLiveActivityMainQuestionBranchLink(t *testing.T) {
	u, _ := newAppServerTestUI()
	v := u.view
	question := "The original request"
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "You", Kind: "text", Text: question, Observed: time.Now()}}})
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 2, Agent: "Main", Kind: "text", Text: strings.Repeat("Working through the request.\n", 20)}}})
	v.applyJournal("root", nativeJournalPublication{item: journalItem{ID: "amber", Text: "The answer", Question: question}, terminal: true, batch: 1})
	if err := u.paint(&bytes.Buffer{}, 120, 24); err != nil {
		t.Fatal(err)
	}
	clicked := false
	for row, target := range v.feedQuestions {
		if target == 1 {
			x := u.shell.layout.codex.x + v.feedLeft + 2
			y := u.shell.layout.codex.y + v.feedTop + row
			if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%dM", x, y)); err != nil {
				t.Fatal(err)
			}
			clicked = true
			break
		}
	}
	if !clicked || v.following || v.offset != v.questionRows[1] {
		t.Fatal("reply branch did not navigate through the terminal mouse route")
	}
	var frame bytes.Buffer
	if err := u.paint(&frame, 120, 24); err != nil || !strings.Contains(frame.String(), question) {
		t.Fatalf("original question not visible after jump: %v", err)
	}
	// Editing an answer after the same prompt is submitted again must retain
	// its original target, not jump to the newer identical question.
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: v.lastSeq + 1, Agent: "You", Kind: "text", Text: question}}})
	v.applyJournal("root", nativeJournalPublication{item: journalItem{ID: "amber", Text: "Edited answer", Question: question}, terminal: true, batch: 2})
	for _, blocks := range v.blocks {
		for _, block := range blocks {
			if block.journal != nil && block.journal.groups[0].target != 1 {
				t.Fatal("answer edit changed its original question link")
			}
		}
	}
}

func TestAppServerActivityExcludesMain(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	u.agents.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
		{Seq: 1, Agent: "/root", Kind: "text", Text: "MAIN_ONLY"},
		{Seq: 2, Agent: "/root/reviewer", Kind: "text", Text: "CHILD_ONLY"},
	}, Agents: []activityPaneAgent{{Name: "/root"}, {Name: "/root/reviewer"}}})
	feed := strings.Join(u.agents.renderFeed(80, 20).lines, "\n")
	if strings.Contains(feed, "MAIN_ONLY") || !strings.Contains(feed, "CHILD_ONLY") || len(u.agents.roster()) != 2 {
		t.Fatal("Activity duplicated Main or removed its roster entry")
	}
}

func TestAppServerMainRosterSummary(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	u.status = "Working"
	u.shell.focus = 3 // Narrow layout displays only the roster, not Main.
	if err := u.paint(&bytes.Buffer{}, 80, 40); err != nil {
		t.Fatal(err)
	}
	root := activityPaneAgent{Name: "/root"}
	if got, _ := u.agents.current(root, time.Now()); ansi.Strip(got) != "Working" {
		t.Fatalf("Main status missing: %q", got)
	}
	u.view.applyAppServerItem("main", "main", "turn", "answer", "item/agentMessage/delta", "Checking the roster", appServerItem{})
	if got, _ := u.agents.current(root, time.Now()); ansi.Strip(got) != "Checking the roster" {
		t.Fatalf("Main streaming summary missing: %q", got)
	}
	u.view.applyAppServerItem("main", "main", "turn", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: "Roster fixed"})
	if got, _ := u.agents.current(root, time.Now()); ansi.Strip(got) != "Roster fixed" {
		t.Fatalf("Main completed summary stale: %q", got)
	}
	if len(u.agents.entries) != 0 {
		t.Fatal("Main summary was copied into Activity")
	}
}

func TestAppServerActivityReasoningSummaries(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	legacy := newLiveActivityView()
	for seq, text := range []string{"Initial public summary", "Updated public summary"} {
		event := activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: uint64(seq + 1), Agent: "/root/reviewer", Kind: "reasoning", CallID: "summary-1", Text: text}}}
		u.agents.apply(event)
		legacy.apply(event)
		u.view.apply(event)
	}
	feed := ansi.Strip(strings.Join(u.agents.renderFeed(80, 20).lines, "\n"))
	status, _ := u.agents.current(activityPaneAgent{Name: "/root/reviewer"}, time.Now())
	if strings.Contains(feed, "public summary") || !strings.Contains(status, "Updated public summary") || len(u.agents.entries) != 1 {
		t.Fatal("live summary must update status, not compact history")
	}
	u.agents.agents = []activityPaneAgent{{Name: "/root/reviewer", Responding: true}}
	live := ansi.Strip(strings.Join(u.agents.renderFeed(80, 20).lines, "\n"))
	if !strings.Contains(live, "◐ Updated public summary") {
		t.Fatal("active reasoning status is missing from Activity")
	}
	u.agents.agents[0].Responding = false
	if idle := strings.Join(u.agents.renderFeed(80, 20).lines, "\n"); strings.Contains(idle, "Updated public summary") {
		t.Fatal("transient reasoning status remained after completion")
	}
	u.agents.only, u.agents.selected = true, "/root/reviewer"
	detail := strings.Join(u.agents.renderFeed(80, 20).lines, "\n")
	if !strings.Contains(detail, "Updated public summary") || !strings.Contains(detail, "\x1b[2;3m") || strings.Contains(detail, "Reasoning summary") {
		t.Fatal("detailed summary must use Codex's dim italic body, not a labelled card")
	}
	if len(legacy.entries) != 0 || len(u.view.entries) != 0 {
		t.Fatal("changed Main or legacy reasoning policy")
	}
}

func TestAppServerCommunicationBothAudiences(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	assignment := "Check the original question link."
	u.applyActivity([]activityPaneEntry{
		{Seq: 1, Agent: "/root/reviewer", Kind: "reply", Text: "[`/root` -> `/root/reviewer`] Message received:\n" + assignment},
		{Seq: 2, Agent: "/root/reviewer", Kind: "reply", Text: "[`/root/reviewer` -> `/root`] Message received:\nI checked the link."},
		{Seq: 3, Agent: "/root/reviewer", Kind: "final", Text: "The question link is correct."},
	}, nil)
	u.view.conversation = true
	main := ansi.Strip(strings.Join(u.view.renderFeed(90, 40).lines, "\n"))
	agents := ansi.Strip(strings.Join(u.agents.renderFeed(90, 40).lines, "\n"))
	for _, body := range []string{assignment, "I checked the link.", "The question link is correct."} {
		if !strings.Contains(main, body) || !strings.Contains(agents, body) {
			t.Fatalf("communication missing from one audience: %q", body)
		}
	}
	if !strings.Contains(main, "→ reviewer") || !strings.Contains(main, "← reviewer") || strings.Contains(main, "Message sent") || strings.Contains(main, "not loaded") || !strings.Contains(agents, "main → reviewer") || !strings.Contains(agents, "reviewer → main") {
		t.Fatalf("wrong message direction or assignment link:\n%s\n%s", main, agents)
	}
}

func TestAppServerOutgoingMessageHeader(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	u.view.conversation = true
	u.applyActivity([]activityPaneEntry{{Seq: 1, Agent: "/root/reviewer", Kind: "reply", Text: "[`/root` -> `/root/reviewer`] Message received:\nCheck the answer link."}}, nil)
	feed := u.view.renderFeed(90, 20)
	if len(feed.lines) != 2 || !strings.HasPrefix(ansi.Strip(feed.lines[0]), "→ reviewer ") || ansi.Strip(feed.lines[1]) != "│ Check the answer link." {
		t.Fatalf("outgoing recipient must head Main's message, above its body: %q", feed.lines)
	}
	screen := vt.NewEmulator(90, 2)
	defer screen.Close()
	if _, err := screen.Write([]byte(feed.lines[0])); err != nil {
		t.Fatal(err)
	}
	if screen.CellAt(0, 0).Content != "→" || screen.CellAt(1, 0).Content != " " || screen.CellAt(2, 0).Content != "r" {
		t.Fatal("arrow must occupy one cell with a visible space before the recipient")
	}
}

func TestAppServerActivityGroupsAgentRun(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	start := time.Date(2026, 9, 26, 12, 30, 10, 0, time.Local)
	u.applyActivity([]activityPaneEntry{
		{Seq: 1, Agent: "/root/reviewer", Kind: "reply", Text: "[`/root` -> `/root/reviewer`] Message received:\nCheck the link.", Observed: start},
		{Seq: 2, Agent: "/root/reviewer", Kind: "reply", Text: "[`/root/reviewer` -> `/root`] Message received:\nLink checked.", Observed: start.Add(10 * time.Second)},
		{Seq: 3, Agent: "/root/reviewer", Kind: "text", Text: "Checking the next item.", Observed: start.Add(20 * time.Second)},
	}, nil)
	u.agents.only, u.agents.selected = true, "/root/reviewer"
	var plain []string
	for _, line := range u.agents.renderFeed(90, 40).lines {
		plain = append(plain, strings.TrimRight(ansi.Strip(line), " "))
	}
	// One stable heading per agent run; each event names sender and recipient on its
	// own row, with its body below and a spacer before the next event.
	want := []string{"reviewer", "│ main → reviewer", "│ Check the link.", "│", "│ reviewer → main", "│ Link checked.", "│", "│ Checking the next item."}
	if len(plain) != len(want) || !strings.HasSuffix(plain[0], "12:30:10") || !strings.HasPrefix(plain[0], want[0]+" ") {
		t.Fatalf("Activity run = %q", plain)
	}
	for i := 1; i < len(want); i++ {
		if plain[i] != want[i] {
			t.Fatalf("Activity run row %d = %q, want %q in %q", i, plain[i], want[i], plain)
		}
	}
}

func TestCodexReasoningSummaryPresentation(t *testing.T) {
	for _, tc := range []struct{ text, header, body string }{
		{"**Checking tests**\n\nInspecting the output.", "Inspecting the output.", "Inspecting the output."},
		{"**Checking tests**: running suite", "Checking tests: running suite", "**Checking tests**: running suite"},
		{"# Checking tests\n<!-- -->", "Checking tests", "# Checking tests\n<!-- -->"},
		{"**Checking tests**\n<!-- -->", "Checking tests", ""},
		{"**Unfinished heading", "Thinking", "**Unfinished heading"},
	} {
		if reasoningSummaryHeader(tc.text) != tc.header || reasoningSummaryBody(tc.text) != tc.body {
			t.Fatalf("Codex summary parsing differs for %q", tc.text)
		}
	}
}

func TestAppServerNativeAssignmentQuestionLink(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	assignment := "Check the original question link."
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"model": "gpt-6-astra", "input": []any{journalTestAssignment("/root/reviewer", "NEW_TASK", assignment)}}))
	if err != nil {
		t.Fatal(err)
	}
	var child strings.Builder
	child.WriteString("Journal result `/root/reviewer`")
	writeJournalItems(&child, []journalItem{{ID: "amber", Question: assignment, Text: "The question link is correct."}})
	u.applyActivity([]activityPaneEntry{
		{Seq: 1, Agent: "/root/reviewer", Kind: "start", Text: subagentStartCommentary(&request, "/root/reviewer")},
		{Seq: 2, Agent: "/root/reviewer", Kind: "final", Text: child.String()},
	}, nil)
	u.view.conversation = true
	main := ansi.Strip(strings.Join(u.view.renderFeed(90, 40).lines, "\n"))
	agents := ansi.Strip(strings.Join(u.agents.renderFeed(90, 40).lines, "\n"))
	if !strings.Contains(main, assignment) || !strings.Contains(agents, assignment) || !strings.Contains(main, "↩ re: assignment") || strings.Contains(main, "not loaded") {
		t.Fatalf("native NEW_TASK assignment was not linked:\n%s\n%s", main, agents)
	}
}

func TestLiveActivityQuestionLinksKeepIndividualPrompts(t *testing.T) {
	v := newLiveActivityView()
	v.conversation, v.feedOnly = true, true
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
		{Seq: 1, Agent: "You", Kind: "text", Text: strings.Repeat("Long original prompt\n", 30)},
		{Seq: 2, Agent: "You", Kind: "text", Text: "Steering prompt"},
	}})
	v.applyJournal("root", nativeJournalPublication{item: journalItem{ID: "amber", Question: "Steering prompt", Text: "Live answer"}})
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: v.lastSeq + 1, Agent: "You", Kind: "text", Text: "Steering prompt"}}})
	v.applyJournal("root", nativeJournalPublication{item: journalItem{ID: "amber", Question: "Steering prompt", Text: "Final answer body"}, terminal: true, batch: 1})
	v.render(80, 12, time.Now())
	if v.questionRows[1] == v.questionRows[2] {
		t.Fatal("consecutive user entries share a navigation target")
	}
	for row, target := range v.feedQuestions {
		if target == 2 {
			v.handleMouse('\r', v.feedTop+row, v.feedLeft+2)
			frame := strings.Join(v.render(80, 12, time.Now()), "\n")
			if !strings.Contains(frame, "Steering prompt") || v.questionRows[2] < v.offset || v.questionRows[2] >= v.offset+v.feedRows || v.offset > max(0, v.feedLines-v.feedRows) {
				t.Fatal("link did not navigate to the individual original steering prompt")
			}
			if v.flashQuestion != 2 || !strings.Contains(frame, v.painter.theme.SelectionBackground()) {
				t.Fatal("clicked original question did not flash")
			}
			if !v.expireFlash(v.flashUntil) {
				t.Fatal("flash expiry did not request a repaint")
			}
			if restored := strings.Join(v.render(80, 12, time.Now()), "\n"); strings.Contains(restored, v.painter.theme.SelectionBackground()) || ansi.Strip(restored) != ansi.Strip(frame) {
				t.Fatal("flash did not restore the original message without changing its contents")
			}
			return
		}
	}
	t.Fatal("live-to-terminal answer lost its original question link")
}

func TestLiveActivityConversationKeepsFullTextAndViewport(t *testing.T) {
	v := newLiveActivityView()
	text := strings.Repeat("visible line\n", 30) + "END_OF_ANSWER"
	v.applyAppServerItem("root", "root", "turn", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: text})
	compact := v.renderFeed(80, 9)
	v.conversation = true
	full := v.renderFeed(80, 9)
	if len(full.lines) <= len(compact.lines) || !strings.Contains(strings.Join(full.lines, "\n"), "END_OF_ANSWER") {
		t.Fatal("Main inherited compact answer clipping")
	}
	u, _ := newAppServerTestUI()
	u.view = v
	if err := u.paint(&bytes.Buffer{}, 80, 15); err != nil {
		t.Fatal(err)
	}
	appServerTestKeys(t, u, "\x1b[5~")
	if v.following {
		t.Fatal("Main did not pause the activity viewport")
	}
	offset := v.offset
	v.applyAppServerItem("root", "root", "turn", "next", "item/completed", "", appServerItem{Type: "agentMessage", Text: "new answer"})
	if err := u.paint(&bytes.Buffer{}, 80, 15); err != nil {
		t.Fatal(err)
	}
	if v.following || v.offset != offset || v.unseen != 1 {
		t.Fatal("Main discarded activity scroll/unseen state")
	}
}

func TestLiveActivityMainShortTerminalKeepsNewestLine(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.applyAppServerItem("root", "root", "turn", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: "earlier paragraph\n\nLATEST_VISIBLE_LINE"})
	var frame bytes.Buffer
	if err := u.paint(&frame, 80, 6); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ansi.Strip(frame.String()), "LATEST_VISIBLE_LINE") {
		t.Fatalf("short Main dropped newest Activity row: %s", ansi.Strip(frame.String()))
	}
}

func TestAppServerReusesTerminalShellAndJournalRenderer(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.applyJournal("root", nativeJournalPublication{item: journalItem{ID: "a", Text: "Milestone", Created: 1, Updated: 1}, terminal: true, batch: 3})
	u.view.applyJournal("root", nativeJournalPublication{item: journalItem{ID: "b", Text: "Answer", Question: "Question", Created: 2, Updated: 2}, terminal: true, batch: 3})
	if len(u.view.entries) != 1 || len(u.view.blocks[0]) != 1 || u.view.blocks[0][0].kind != "final" || len(u.view.blocks[0][0].journal.groups) != 2 {
		t.Fatal("native batch did not use the existing grouped Activity journal result")
	}
	if err := u.paint(&bytes.Buffer{}, 120, 30); err != nil {
		t.Fatal(err)
	}
	if u.shell.layout.codex.w != 58 || u.shell.layout.agents.x != 61 || u.shell.layout.vertical != 60 {
		t.Fatalf("native UI changed the existing pane positions: %+v", u.shell.layout)
	}
	if err := u.paint(&bytes.Buffer{}, 100, 4); err != nil {
		t.Fatal(err)
	}
	if u.mainContentPainted {
		t.Fatal("empty Main body must not acknowledge journal delivery")
	}
}

func TestLiveActivityMainJournalContentOnly(t *testing.T) {
	v := newLiveActivityView()
	v.applyJournal("root", nativeJournalPublication{item: journalItem{ID: "amber", Text: "Milestone body", Created: 1}, terminal: true, batch: 1})
	v.applyJournal("root", nativeJournalPublication{item: journalItem{ID: "apple", Text: "Answer body", Question: "Original question", Created: 2}, terminal: true, batch: 1})
	v.only = true
	v.selected = "Main"
	agents := ansi.Strip(strings.Join(v.renderFeed(80, 20).lines, "\n"))
	v.conversation = true
	main := ansi.Strip(strings.Join(v.renderFeed(80, 20).lines, "\n"))
	if !strings.Contains(agents, "✓ Final answer") || strings.Contains(main, "Final answer") {
		t.Fatal("Main heading change affected Agents or reused stale cache")
	}
	for _, hidden := range []string{"Original question", "amber", "apple"} {
		if strings.Contains(main, hidden) || !strings.Contains(agents, hidden) {
			t.Fatalf("journal metadata %q must stay in Agents, not Main", hidden)
		}
	}
	for _, body := range []string{"Milestone body", "Answer body"} {
		if strings.Count(main, body) != 1 || !strings.Contains(agents, body) {
			t.Fatalf("journal content %q missing or duplicated", body)
		}
	}
}

func TestAppServerJournalRetractionKeepsSiblingLinks(t *testing.T) {
	v := newLiveActivityView()
	v.conversation, v.feedOnly = true, true
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "You", Kind: "text", Text: "Question?", Observed: time.Now()}}})
	for i, id := range []string{"a", "b"} {
		v.applyJournal("t", nativeJournalPublication{item: journalItem{ID: id, Text: "answer " + id, Question: "Question?", Created: uint64(i + 1)}, terminal: true, batch: 1})
	}
	v.applyJournal("t", nativeJournalPublication{item: journalItem{ID: "b", Question: "Question?"}, terminal: true, retracted: true, batch: 1})
	frame := ansi.Strip(strings.Join(v.render(60, 12, time.Now()), "\n"))
	if !strings.Contains(frame, "↩ re: your message") || strings.Contains(frame, "not loaded") || strings.Contains(frame, "answer b") {
		t.Fatalf("retraction lost the surviving answer's link:\n%s", frame)
	}
}

func TestAppServerMainDoesNotPinItemFirstRow(t *testing.T) {
	v := newLiveActivityView()
	v.conversation, v.feedOnly = true, true
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "You", Kind: "text", Text: "Question?", Observed: time.Now()}}})
	v.applyJournal("t", nativeJournalPublication{item: journalItem{ID: "a", Text: strings.Repeat("answer row\n\n", 20), Question: "Question?", Created: 1}, terminal: true, batch: 1})
	v.render(60, 60, time.Now())
	v.offset, v.following = 6, false
	lines := v.render(60, 8, time.Now())
	if strings.Contains(ansi.Strip(lines[0]), "↩") {
		t.Fatalf("Main pinned an item's first row over its middle: %q", ansi.Strip(lines[0]))
	}
}

func TestAppServerActivityHeadingShowsLateRole(t *testing.T) {
	v := newLiveActivityView()
	v.childrenOnly = true
	v.apply(activityPaneEvent{Kind: "entries", Agents: []activityPaneAgent{{Name: "/root/probe"}},
		Entries: []activityPaneEntry{{Seq: 1, Agent: "/root/probe", Kind: "text", Text: "hello", Observed: time.Now()}}})
	v.render(60, 8, time.Now())
	v.apply(activityPaneEvent{Kind: "agents", Agents: []activityPaneAgent{{Name: "/root/probe", Role: "explorer"}}})
	if frame := ansi.Strip(strings.Join(v.render(60, 8, time.Now()), "\n")); !strings.Contains(frame, "probe · explorer") {
		t.Fatalf("heading kept a stale role:\n%s", frame)
	}
}
