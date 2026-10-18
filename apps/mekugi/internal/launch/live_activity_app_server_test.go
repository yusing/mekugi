package launch

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestConversationJoinsConsecutiveUserMessagesInTurn(t *testing.T) {
	v := newActivityAdapter()
	v.Conversation = true
	v.testAudience()
	user := func(turn, id, text string) {
		applyTranscriptFixture(v, true, "", "main", "main", turn, id, "item/completed", "", binding.ThreadItem{
			Type: "userMessage", Content: []byte(fmt.Sprintf(`[{"type":"text","text":%q}]`, text)),
		})
	}
	user("t", "one", strings.Repeat("Also make sure different unit types have different colors.\n", 20))
	user("t", "two", "I do not want blue for everything")
	feed := v.testRenderConversation(100)
	shown := ansi.Strip(strings.Join(feed.Lines, "\n"))
	if strings.Count(shown, "❯") != 1 || !strings.Contains(shown, "  I do not want blue for everything") || len(feed.Questions) == 0 {
		t.Fatalf("stacked input did not render as one prompt: %q", shown)
	}
	if v.QuestionRows[v.Entries[1].Source.Seq] <= v.QuestionRows[v.Entries[0].Source.Seq]+10 {
		t.Fatalf("stacked input lost its own navigation row: %+v", v.QuestionRows)
	}
	second := v.QuestionRows[v.Entries[1].Source.Seq]
	if feed.Heads[second] != second || feed.Heads[second-1] == second {
		t.Fatalf("continued prompt lost its flash range: heads=%+v target=%d", feed.Heads, second)
	}
	v.Painter.Theme = diffrender.DarkTheme
	v.FeedOnly = true
	v.FlashQuestion, v.FlashUntil = v.Entries[1].Source.Seq, time.Now().Add(time.Second)
	v.Offset = second
	frame := strings.Join(v.Render(100, 12, time.Now()), "\n")
	if !strings.Contains(frame, "I do not want blue for everything") || !strings.Contains(frame, v.Painter.Theme.SelectionBackground()) {
		t.Fatalf("continued prompt not visible and shaded: %q", frame)
	}
	user("next", "three", "A separate turn")
	shown = ansi.Strip(strings.Join(v.testRenderConversation(100).Lines, "\n"))
	if strings.Count(shown, "❯") != 2 {
		t.Fatalf("different turns were joined: %q", shown)
	}
}

func TestLiveActivityNativeItemsShareStateAndRendering(t *testing.T) {
	v := newActivityAdapter()
	applyTranscriptFixture(v, true, "", "root", "root", "turn", "answer", "item/agentMessage/delta", "partial", binding.ThreadItem{})
	first := v.Entries[0].Source.Seq
	applyTranscriptFixture(v, true, "", "root", "root", "turn", "answer", "item/started", "", binding.ThreadItem{Type: "agentMessage"})
	if v.Entries[0].Source.Text != "partial" {
		t.Fatal("late start erased streamed activity")
	}
	applyTranscriptFixture(v, true, "", "root", "root", "turn", "answer", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "complete"})
	applyTranscriptFixture(v, true, "", "root", "root", "turn", "answer", "item/agentMessage/delta", "late", binding.ThreadItem{})
	if len(v.Entries) != 1 || v.Entries[0].Source.Text != "complete" || v.Entries[0].Source.Seq != first || v.blocks(0)[0].Body != "complete" {
		t.Fatal("activity identity/update diverged")
	}
	applyTranscriptFixture(v, true, "", "root", "child", "turn", "answer", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "child"})
	applyTranscriptFixture(v, true, "", "root", "root", "turn", "cmd", "item/completed", "", binding.ThreadItem{Type: "commandExecution", Command: "printf ok", Status: "completed"})
	if len(v.Entries) != 3 || v.Entries[1].Source.Agent != "Thread child" || v.blocks(2)[0].Verb != "Run" || v.blocks(2)[0].Code != "printf ok" {
		t.Fatal("native items did not enter shared activity blocks")
	}
	// Main is the same activity viewport, not another row cache or renderer.
	u, _ := newAppServerTestUI()
	u.shell.Transcript, u.attached.source = v.View, v.ActivitySource
	var frame bytes.Buffer
	if err := u.testPaint(&frame, 100, 25); err != nil {
		t.Fatal(err)
	}
	plain := ansi.Strip(frame.String())
	if !strings.Contains(plain, "┃ complete") || !strings.Contains(plain, "printf ok") || !strings.Contains(plain, "complete") {
		t.Fatalf("Main did not render activity state: %s", plain)
	}
}

func TestLiveActivityMainQuestionBranchLink(t *testing.T) {
	u, _ := newAppServerTestUI()
	v := u.testActivity(u.shell.Transcript)
	question := "The original request"
	v.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "You", Kind: "text", Text: question, Observed: time.Now(), Native: &sessionpresentation.NativeItem{Thread: "root", Turn: "turn", Item: "prompt"}}}})
	v.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 2, Agent: "Main", Kind: "text", Text: strings.Repeat("Working through the request.\n", 20)}}})
	applyTranscriptFixture(v, true, "", "root", "root", "turn", "answer", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "The answer"})
	if err := u.testPaint(&bytes.Buffer{}, 120, 24); err != nil {
		t.Fatal(err)
	}
	clicked := false
	for row, target := range v.FeedQuestions {
		if target == 1 {
			x := terminalTest(u).Layout.Main.X + v.FeedLeft + 2
			y := terminalTest(u).Layout.Main.Y + v.FeedTop + row
			if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<0;%d;%dM", x, y)); err != nil {
				t.Fatal(err)
			}
			if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<0;%d;%dm", x, y)); err != nil {
				t.Fatal(err)
			}
			clicked = true
			break
		}
	}
	if !clicked || terminalTest(u).Dialog == nil || !v.Following {
		t.Fatal("reply branch did not open a dialog while preserving follow")
	}
	var frame bytes.Buffer
	if err := u.testPaint(&frame, 120, 24); err != nil || !strings.Contains(frame.String(), question) {
		t.Fatalf("original question not visible in dialog: %v", err)
	}
	terminalTest(u).outputKey("\x1b")
	if terminalTest(u).Dialog != nil || !v.Following {
		t.Fatal("dialog close changed Main follow")
	}
	// Editing an answer after the same prompt is submitted again must retain
	// its original target, not jump to the newer identical question.
	v.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: v.LastSeq + 1, Agent: "You", Kind: "text", Text: question}}})
	applyTranscriptFixture(v, true, "", "root", "root", "turn", "answer", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "Edited answer"})
	for _, record := range v.Entries {
		if record.Source.Native != nil && record.Source.Native.Item == "answer" && record.Source.Native.Question != 1 {
			t.Fatal("answer edit changed its original question link")
		}
	}
}

func TestAppServerActivityExcludesMain(t *testing.T) {
	u, _ := newAppServerTestUI()

	u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "/root", Kind: "text", Text: "MAIN_ONLY"},
		{Seq: 2, Agent: "/root/reviewer", Kind: "text", Text: "CHILD_ONLY"},
	}, Agents: []sessionpresentation.ActivityAgent{{Name: "/root"}, {Name: "/root/reviewer"}}})
	feed := strings.Join(u.testActivity(u.shell.Agents).RenderFeed(80, 20).Lines, "\n")
	if strings.Contains(feed, "MAIN_ONLY") || !strings.Contains(feed, "CHILD_ONLY") || len(u.testActivity(u.shell.Agents).testRoster()) != 2 {
		t.Fatal("Activity duplicated Main or removed its roster entry")
	}
}

func TestAppServerMainRosterSummary(t *testing.T) {
	u, _ := newAppServerTestUI()

	u.attached.presentation.Status.Text = "Working"
	terminalTest(u).Focus = 3 // Narrow layout displays only the roster, not Main.
	if err := u.testPaint(&bytes.Buffer{}, 80, 40); err != nil {
		t.Fatal(err)
	}
	root := sessionpresentation.ActivityAgent{Name: "/root"}
	if got, _ := u.testActivity(u.shell.Agents).testCurrent(root, time.Now()); ansi.Strip(got) != "Working" {
		t.Fatalf("Main status missing: %q", got)
	}
	applyTranscriptFixture(u.testActivity(u.shell.Transcript), true, "", "main", "main", "turn", "answer", "item/agentMessage/delta", "Checking the roster", binding.ThreadItem{})
	if got, _ := u.testActivity(u.shell.Agents).testCurrent(root, time.Now()); ansi.Strip(got) != "Checking the roster" {
		t.Fatalf("Main streaming summary missing: %q", got)
	}
	applyTranscriptFixture(u.testActivity(u.shell.Transcript), true, "", "main", "main", "turn", "answer", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "Roster fixed"})
	if got, _ := u.testActivity(u.shell.Agents).testCurrent(root, time.Now()); ansi.Strip(got) != "Roster fixed" {
		t.Fatalf("Main completed summary stale: %q", got)
	}
	if len(u.attached.childSource.Entries) != 0 {
		t.Fatal("Main summary was copied into Activity")
	}
}

func TestAppServerActivityReasoningSummaries(t *testing.T) {
	u, _ := newAppServerTestUI()

	legacy := newActivityAdapter()
	for seq, text := range []string{"Initial public summary", "Updated public summary"} {
		event := sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: uint64(seq + 1), Agent: "/root/reviewer", Kind: "reasoning", CallID: "summary-1", Text: text}}}
		u.testActivity(u.shell.Agents).Apply(event)
		legacy.Apply(event)
		u.testActivity(u.shell.Transcript).Apply(event)
	}
	feed := ansi.Strip(strings.Join(u.testActivity(u.shell.Agents).RenderFeed(80, 20).Lines, "\n"))
	status, _ := u.testActivity(u.shell.Agents).testCurrent(sessionpresentation.ActivityAgent{Name: "/root/reviewer"}, time.Now())
	if !strings.Contains(feed, "Updated public summary") || !strings.Contains(status, "Updated public summary") || len(u.attached.childSource.Entries) != 1 {
		t.Fatal("summary must remain in Activity and update status")
	}
	u.attached.childSource.Agents = []sessionpresentation.ActivityAgent{{Name: "/root/reviewer", Responding: true}}
	live := strings.Join(u.testActivity(u.shell.Agents).RenderFeed(80, 20).Lines, "\n")
	if !strings.Contains(ansi.Strip(live), "Updated public summary") {
		t.Fatal("reasoning summary is missing from Activity")
	}
	u.attached.childSource.Agents[0].Responding = false
	if idle := strings.Join(u.testActivity(u.shell.Agents).RenderFeed(80, 20).Lines, "\n"); !strings.Contains(idle, "Updated public summary") {
		t.Fatal("reasoning transcript disappeared after completion")
	}
	u.testActivity(u.shell.Agents).Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{
		{Seq: 3, Agent: "/root/reviewer", Kind: "reasoning", CallID: "summary-2", Text: "Later public summary"},
		{Seq: 4, Agent: "/root", Kind: "reasoning", CallID: "main-summary", Text: "Main only summary"},
	}})
	retained := ansi.Strip(strings.Join(u.testActivity(u.shell.Agents).RenderFeed(80, 30).Lines, "\n"))
	if !strings.Contains(retained, "Updated public summary") || !strings.Contains(retained, "Later public summary") || strings.Contains(retained, "Main only summary") {
		t.Fatalf("Activity lost earlier reasoning or included Main: %q", retained)
	}
	u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/reviewer")
	detail := strings.Join(u.testActivity(u.shell.Agents).RenderFeed(80, 20).Lines, "\n")
	if !strings.Contains(detail, "Updated public summary") || strings.Contains(detail, "Reasoning summary") {
		t.Fatal("detailed summary must remain visible, not a labelled card")
	}
	if len(legacy.Entries) != 0 || len(u.attached.source.Entries) != 0 {
		t.Fatal("changed Main or legacy reasoning policy")
	}
}

func TestAppServerCommunicationBothAudiences(t *testing.T) {
	u, _ := newAppServerTestUI()

	assignment := "Check the original question link."
	applyActivity(&u.attached, &u.approvals, u.shell, u.review, &u.pendingEditReceipts, u.Thread, u.clock, []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "/root/reviewer", Kind: "reply", Message: &sessionpresentation.ActivityMessage{From: "/root", To: "/root/reviewer", Text: assignment}},
		{Seq: 2, Agent: "/root/reviewer", Kind: "reply", Message: &sessionpresentation.ActivityMessage{From: "/root/reviewer", To: "/root", Text: "I checked the link."}},
		{Seq: 3, Agent: "/root/reviewer", Kind: "final", Text: "The question link is correct."},
	}, nil)
	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	main := ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(90, 40).Lines, "\n"))
	agents := ansi.Strip(strings.Join(u.testActivity(u.shell.Agents).RenderFeed(90, 40).Lines, "\n"))
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

	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	applyActivity(&u.attached, &u.approvals, u.shell, u.review, &u.pendingEditReceipts, u.Thread, u.clock, []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "/root/reviewer", Kind: "reply", Message: &sessionpresentation.ActivityMessage{From: "/root", To: "/root/reviewer", Text: "Check the answer link."}}}, nil)
	feed := u.testActivity(u.shell.Transcript).RenderFeed(90, 20)
	if len(feed.Lines) != 2 || !strings.HasPrefix(ansi.Strip(feed.Lines[0]), "→ reviewer") || ansi.Strip(feed.Lines[1]) != "│ Check the answer link." {
		t.Fatalf("outgoing recipient must head Main's message, above its body: %q", feed.Lines)
	}
	screen := vt.NewEmulator(90, 2)
	defer screen.Close()
	if _, err := screen.Write([]byte(feed.Lines[0])); err != nil {
		t.Fatal(err)
	}
	if screen.CellAt(0, 0).Content != "→" || screen.CellAt(1, 0).Content != " " || screen.CellAt(2, 0).Content != "r" {
		t.Fatal("arrow must occupy one cell with a visible space before the recipient")
	}
}

func TestAppServerActivityGroupsAgentRun(t *testing.T) {
	u, _ := newAppServerTestUI()

	start := time.Date(2026, 9, 26, 12, 30, 10, 0, time.Local)
	applyActivity(&u.attached, &u.approvals, u.shell, u.review, &u.pendingEditReceipts, u.Thread, u.clock, []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "/root/reviewer", Kind: "reply", Message: &sessionpresentation.ActivityMessage{From: "/root", To: "/root/reviewer", Text: "Check the link."}, Observed: start},
		{Seq: 2, Agent: "/root/reviewer", Kind: "reply", Message: &sessionpresentation.ActivityMessage{From: "/root/reviewer", To: "/root", Text: "Link checked."}, Observed: start.Add(10 * time.Second)},
		{Seq: 3, Agent: "/root/reviewer", Kind: "text", Text: "Checking the next item.", Observed: start.Add(20 * time.Second)},
	}, nil)
	u.testActivity(u.shell.Agents).setDisplayFilter(true, "/root/reviewer")
	var plain []string
	for _, line := range u.testActivity(u.shell.Agents).RenderFeed(90, 40).Lines {
		plain = append(plain, strings.TrimRight(ansi.Strip(line), " "))
	}
	// Narrative events retain their individual timestamps and message direction.
	if len(plain) != 8 {
		t.Fatalf("Activity events = %q", plain)
	}
	for i, stamp := range []string{"12:30:10", "12:30:20", "12:30:30"} {
		if !strings.HasSuffix(plain[i*3], stamp) {
			t.Fatalf("event lost timestamp: %q", plain[i*3])
		}
	}
	if !strings.Contains(plain[0], "main → reviewer") || !strings.Contains(plain[3], "reviewer → main") {
		t.Fatalf("message direction lost: %q", plain)
	}
	if plain[1] != "│ Check the link." || plain[4] != "│ Link checked." || plain[7] != "│ Checking the next item." {
		t.Fatalf("message bodies lost: %q", plain)
	}

}

func TestCodexReasoningSummaryPresentation(t *testing.T) {
	for _, tc := range []struct{ text, header, body string }{
		{"**Checking tests**\n\nInspecting the output.", "Inspecting the output.", "Inspecting the output."},
		{"**Checking tests**: running suite", "Checking tests: running suite", "**Checking tests**: running suite"},
		{"# Checking tests\n<!-- -->", "Checking tests", "# Checking tests"},
		{"**Checking tests**\n<!-- -->", "Checking tests", "**Checking tests**"},
		{"**Checking tests**\n\n", "Checking tests", "**Checking tests**"},
		{"**Unfinished heading", "Thinking", "**Unfinished heading"},
	} {
		if activitypresentation.ReasoningSummaryHeader(tc.text) != tc.header || activitypresentation.ReasoningSummaryBody(tc.text) != tc.body {
			t.Fatalf("Codex summary parsing differs for %q", tc.text)
		}
	}
}

func TestAppServerNativeAssignmentQuestionLink(t *testing.T) {
	u, _ := newAppServerTestUI()

	assignment := "Check the original question link."
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"model": "gpt-6-astra", "input": []any{nativeAssignmentTestMessage("/root/reviewer", "NEW_TASK", assignment)}}))
	if err != nil {
		t.Fatal(err)
	}
	applyActivity(&u.attached, &u.approvals, u.shell, u.review, &u.pendingEditReceipts, u.Thread, u.clock, []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "/root/reviewer", Kind: "start", Start: testActivityStart(t, &request),
			Assignment: &sessionpresentation.ActivityAssignment{ID: "task-1", From: "/root", To: "/root/reviewer", Text: assignment}},
		{Seq: 2, Agent: "/root/reviewer", Kind: "final", Text: "The question link is correct."},
	}, nil)
	u.shell.Transcript.Conversation = true
	u.testActivity(u.shell.Transcript).testAudience()
	main := ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(90, 40).Lines, "\n"))
	agents := ansi.Strip(strings.Join(u.testActivity(u.shell.Agents).RenderFeed(90, 40).Lines, "\n"))
	if !strings.Contains(main, assignment) || !strings.Contains(agents, assignment) || !strings.Contains(main, "├─✓ finished") || strings.Contains(main, "↩ re:") {
		t.Fatalf("native NEW_TASK answer was not threaded under its assignment:\n%s\n%s", main, agents)
	}
}

func TestLiveActivityQuestionLinksKeepIndividualPrompts(t *testing.T) {
	v := newActivityAdapter()
	v.Conversation, v.FeedOnly = true, true
	v.testAudience()
	v.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "You", Kind: "text", Text: strings.Repeat("Long original prompt\n", 30)},
		{Seq: 2, Agent: "You", Kind: "text", Text: "Steering prompt", Native: &sessionpresentation.NativeItem{Thread: "root", Turn: "turn", Item: "steer"}},
	}})
	applyTranscriptFixture(v, true, "", "root", "root", "turn", "answer", "item/agentMessage/delta", "Live answer", binding.ThreadItem{})
	v.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: v.LastSeq + 1, Agent: "You", Kind: "text", Text: "Steering prompt"}}})
	applyTranscriptFixture(v, true, "", "root", "root", "turn", "answer", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "Final answer body"})
	v.Render(80, 12, time.Now())
	if v.QuestionRows[1] == v.QuestionRows[2] {
		t.Fatal("consecutive user entries share a navigation target")
	}
	for row, target := range v.FeedQuestions {
		if target == 2 {
			// The pointer underlines the link row it rests on.
			if !v.testHandleMouse('h', v.FeedTop+row, v.FeedLeft+2) || !strings.Contains(v.Render(80, 12, time.Now())[v.FeedTop-1+row], "\x1b[4m↩ re:") {
				t.Fatal("hovered question link was not underlined")
			}
			v.testHandleMouse('\r', v.FeedTop+row, v.FeedLeft+2)
			frame := strings.Join(v.Render(80, 12, time.Now()), "\n")
			if !strings.Contains(frame, "Steering prompt") || v.QuestionRows[2] < v.Offset || v.QuestionRows[2] >= v.Offset+v.FeedRows || v.Offset > max(0, v.FeedLines-v.FeedRows) {
				t.Fatal("link did not navigate to the individual original steering prompt")
			}
			if v.FlashQuestion != 2 || !strings.Contains(frame, v.Painter.Theme.SelectionBackground()) {
				t.Fatal("clicked original question did not flash")
			}
			if !v.ExpireFlash(v.FlashUntil) {
				t.Fatal("flash expiry did not request a repaint")
			}
			if restored := strings.Join(v.Render(80, 12, time.Now()), "\n"); strings.Contains(restored, v.Painter.Theme.SelectionBackground()) || ansi.Strip(restored) != ansi.Strip(frame) {
				t.Fatal("flash did not restore the original message without changing its contents")
			}
			return
		}
	}
	t.Fatal("live-to-terminal answer lost its original question link")
}

func TestLiveActivityConversationKeepsFullTextAndViewport(t *testing.T) {
	v := newActivityAdapter()
	text := strings.Repeat("visible line\n", 30) + "END_OF_ANSWER"
	applyTranscriptFixture(v, true, "", "root", "root", "turn", "answer", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: text})
	compact := v.RenderFeed(80, 9)
	v.Conversation = true
	v.testAudience()
	full := v.RenderFeed(80, 9)
	if len(full.Lines) <= len(compact.Lines) || !strings.Contains(strings.Join(full.Lines, "\n"), "END_OF_ANSWER") {
		t.Fatal("Main inherited compact answer clipping")
	}
	u, _ := newAppServerTestUI()
	u.shell.Transcript, u.attached.source = v.View, v.ActivitySource
	if err := u.testPaint(&bytes.Buffer{}, 80, 15); err != nil {
		t.Fatal(err)
	}
	appServerTestKeys(t, u, "\x1b[5~")
	if v.Following {
		t.Fatal("Main did not pause the activity viewport")
	}
	offset := v.Offset
	applyTranscriptFixture(v, true, "", "root", "root", "turn", "next", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "new answer"})
	if err := u.testPaint(&bytes.Buffer{}, 80, 15); err != nil {
		t.Fatal(err)
	}
	if v.Following || v.Offset != offset || v.Unseen != 1 {
		t.Fatal("Main discarded activity scroll/unseen state")
	}
}

func TestLiveActivityMainShortTerminalKeepsNewestLine(t *testing.T) {
	u, _ := newAppServerTestUI()
	applyTranscriptFixture(u.testActivity(u.shell.Transcript), true, "", "root", "root", "turn", "answer", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "earlier paragraph\n\nLATEST_VISIBLE_LINE"})
	var frame bytes.Buffer
	if err := u.testPaint(&frame, 80, 6); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ansi.Strip(frame.String()), "LATEST_VISIBLE_LINE") {
		t.Fatalf("short Main dropped newest Activity row: %s", ansi.Strip(frame.String()))
	}
}

func TestAppServerMainDoesNotPinItemFirstRow(t *testing.T) {
	v := newActivityAdapter()
	v.Conversation, v.FeedOnly = true, true
	v.testAudience()
	v.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "You", Kind: "text", Text: "Question?", Observed: time.Now()}}})
	applyTranscriptFixture(v, true, "", "root", "root", "turn", "answer", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: strings.Repeat("answer row\n\n", 20)})
	v.Render(60, 60, time.Now())
	v.Offset, v.Following = 6, false
	lines := v.Render(60, 8, time.Now())
	if strings.Contains(ansi.Strip(lines[0]), "↩") {
		t.Fatalf("Main pinned an item's first row over its middle: %q", ansi.Strip(lines[0]))
	}
}

func TestAppServerActivityHeadingShowsLateRole(t *testing.T) {
	v := newActivityAdapter()
	v.ChildrenOnly = true
	v.testAudience()
	v.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Agents: []sessionpresentation.ActivityAgent{{Name: "/root/probe"}},
		Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "/root/probe", Kind: "text", Text: "hello", Observed: time.Now()}}})
	v.Render(60, 8, time.Now())
	v.Apply(sessionpresentation.ActivityEvent{Kind: "agents", Agents: []sessionpresentation.ActivityAgent{{Name: "/root/probe", Role: "explorer"}}})
	if frame := ansi.Strip(strings.Join(v.Render(60, 8, time.Now()), "\n")); !strings.Contains(frame, "probe · explorer") {
		t.Fatalf("heading kept a stale role:\n%s", frame)
	}
}

func TestLiveActivityMainSymbolicReadPaths(t *testing.T) {
	for _, phase := range []string{"item/started", "item/completed"} {
		t.Run(phase, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			command := `mcat "$HOME/.codex/INSTRUCTION-AUTHORING.md" AGENTS.md`
			appServerTestNotify(t, u, phase, map[string]any{"threadId": "main", "turnId": "turn", "item": binding.ThreadItem{ID: "read", Type: "commandExecution", Command: command}})
			var frame bytes.Buffer
			if err := u.testPaint(&frame, 140, 25); err != nil {
				t.Fatal(err)
			}
			plain := ansi.Strip(frame.String())
			if !strings.Contains(plain, "Read") || !strings.Contains(plain, "$HOME/.codex/INSTRUCTION-AUTHORING.md") || !strings.Contains(plain, "AGENTS.md") || strings.Contains(plain, "Ran") {
				t.Fatalf("symbolic read leaked as command: %s", plain)
			}
		})
	}
}

func TestAppServerPacesBatchedOperations(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	item := map[string]any{"id": "batch", "type": "commandExecution", "command": "ls; rg needle; cat a.go", "commandActions": []map[string]any{
		{"type": "listFiles", "path": "."}, {"type": "search", "query": "needle"}, {"type": "read", "path": "a.go"}}}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	frame := func() string {
		return ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).Render(100, 20, time.Now()), "\n"))
	}
	if got := frame(); !strings.Contains(got, "List") || strings.Contains(got, "needle") || strings.Contains(got, "a.go") {
		t.Fatalf("batch did not start with its first operation:\n%s", got)
	}
	now := time.Now()
	if u.testActivity(u.shell.Transcript).testPace(now) {
		t.Fatal("paced before the step elapsed")
	}
	if !u.testActivity(u.shell.Transcript).testPace(now.Add(activityui.PaceStep)) {
		t.Fatal("second operation did not reveal after one step")
	}
	if got := frame(); !strings.Contains(got, "needle") || strings.Contains(got, "a.go") {
		t.Fatalf("second step revealed the wrong operations:\n%s", got)
	}
	u.testActivity(u.shell.Transcript).testPace(now.Add(3 * activityui.PaceStep))
	if got := frame(); !strings.Contains(got, "a.go") || len(u.shell.Transcript.Paced) != 0 {
		t.Fatalf("batch did not finish revealing (paced=%v):\n%s", u.shell.Transcript.Paced, got)
	}

	// Restored history shows at once.
	restored := newActivityAdapter()
	restored.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "tool", Text: "List `.`\n\nSearch `needle`",
		Native: &sessionpresentation.NativeItem{Thread: "main", Turn: "t", Item: "old", Phase: "item/completed"}}}})
	if len(restored.Paced) != 0 {
		t.Fatal("restored history was paced")
	}
}

// finishPacing reveals every paced operation, as frames eventually do.
func finishPacing(views ...*activityAdapter) {
	for _, view := range views {
		view.testPace(time.Now().Add(time.Hour))
	}
}
