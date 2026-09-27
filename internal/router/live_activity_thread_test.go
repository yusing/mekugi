package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// threadTestView is Main with a long assignment and the agent's long reply.
func threadTestView() *liveActivityView {
	start := time.Date(2026, 9, 27, 21, 40, 8, 0, time.Local)
	task := strings.Repeat("Review the follow-scroll changes read-only. ", 12)
	reply := strings.Repeat("Inspection found no behavioral failure. ", 16)
	v := newLiveActivityView()
	v.conversation, v.feedOnly = true, true
	v.entries = []activityPaneEntry{
		{Seq: 1, Agent: "Main", Kind: "start", Observed: start, assignment: &activityAssignment{to: "/root/reviewer", text: task}},
		{Seq: 2, Agent: "/root/reviewer", Kind: "reply", Observed: start.Add(58 * time.Second), activitySeq: 9},
	}
	v.blocks = [][]activityui.Block{
		{{Kind: "start", From: "/root", To: "/root/reviewer", Label: "gpt-6-astra", Body: task}},
		{{Kind: "message", From: "/root/reviewer", To: "/root", Body: reply}},
	}
	return v
}

func threadPlain(feed liveActivityFeed) []string {
	plain := make([]string, len(feed.lines))
	for i, line := range feed.lines {
		plain[i] = ansi.Strip(line)
	}
	return plain
}

func TestConversationThreadsReplyUnderAssignment(t *testing.T) {
	const width = 60
	v := threadTestView()
	feed := v.renderFeed(width, 40)
	plain := threadPlain(feed)
	want := []string{"▶ reviewer started · gpt-6-astra", "│ ", "│ ", "├─← replied · 58s", "│ ", "│ ", "│ ", "│ ", "╰─↩ Open reply in Activity · +"}
	if len(plain) != len(want) {
		t.Fatalf("thread rows = %d, want %d:\n%s", len(plain), len(want), strings.Join(plain, "\n"))
	}
	for i, prefix := range want {
		if !strings.HasPrefix(plain[i], prefix) || ansi.StringWidth(feed.lines[i]) > width {
			t.Fatalf("row %d = %q, want prefix %q:\n%s", i, plain[i], prefix, strings.Join(plain, "\n"))
		}
	}
	if !strings.HasSuffix(plain[0], "21:40:08") || strings.Contains(plain[3], "21:4") {
		t.Fatalf("reply should show elapsed time instead of a second clock: %q / %q", plain[0], plain[3])
	}
	if !strings.Contains(plain[2], "… ") || !strings.HasSuffix(plain[2], " lines") {
		t.Fatalf("collapsed assignment lost its ellipsis or count: %q", plain[2])
	}
	if !strings.HasSuffix(plain[7], "…") || feed.questions[8] != 2 {
		t.Fatalf("reply excerpt or its Activity link is wrong: %q, link %d", plain[7], feed.questions[8])
	}
	snippet := feed.snippets[1]
	if snippet != (liveActivitySnippet{1, 0}) || feed.snippets[2] != snippet || feed.questions[1] != 0 {
		t.Fatalf("collapsed assignment is not expandable: %+v", feed.snippets[:3])
	}

	v.toggleSnippet(snippet)
	expanded := threadPlain(v.renderFeed(width, 40))
	if len(expanded) <= len(plain) || !strings.Contains(strings.Join(expanded, "\n"), "├─← replied") || strings.Contains(expanded[2], " lines") {
		t.Fatalf("expanding the assignment did not show it in full:\n%s", strings.Join(expanded, "\n"))
	}
	v.toggleSnippet(snippet)
	if again := threadPlain(v.renderFeed(width, 40)); len(again) != len(plain) {
		t.Fatalf("collapsing again did not restore the excerpt:\n%s", strings.Join(again, "\n"))
	}
}

func TestConversationThreadEndsAtOtherTraffic(t *testing.T) {
	v := threadTestView()
	// Pending: the assignment is the latest item, so it stays in full.
	v.entries, v.blocks = v.entries[:1], v.blocks[:1]
	pending := threadPlain(v.renderFeed(60, 40))
	if strings.Contains(strings.Join(pending, "\n"), "lines") || len(pending) < 5 {
		t.Fatalf("an unanswered assignment was collapsed:\n%s", strings.Join(pending, "\n"))
	}
	// Another agent's traffic in between ends the thread; the reply keeps its
	// own heading, and the closing connector.
	v = threadTestView()
	other := activityPaneEntry{Seq: 3, Agent: "/root/other", Kind: "reply", Observed: v.entries[1].Observed, activitySeq: 8}
	v.entries = []activityPaneEntry{v.entries[0], other, v.entries[1]}
	v.blocks = [][]activityui.Block{v.blocks[0], {{Kind: "message", From: "/root/other", To: "/root", Body: "Unrelated."}}, v.blocks[1]}
	text := strings.Join(threadPlain(v.renderFeed(60, 40)), "\n")
	if strings.Contains(text, "├─") || strings.Contains(text, " lines\n") || !strings.Contains(text, "← reviewer") {
		t.Fatalf("non-adjacent traffic joined a thread:\n%s", text)
	}
}

func TestConversationThreadQuotesEarlierAssignment(t *testing.T) {
	v := newLiveActivityView()
	v.conversation, v.feedOnly = true, true
	task := func(seq uint64, text string) activityPaneEntry {
		return activityPaneEntry{Seq: seq, Agent: "Main", Kind: "assignment", Observed: time.Now(), assignment: &activityAssignment{to: "/root/reviewer", text: text}}
	}
	answer := &activityui.Journal{Groups: []activityui.AnswerGroup{{Question: "First task.", Target: 1, Answers: []activityui.Answer{{Text: "First answer."}}}}}
	v.entries = []activityPaneEntry{task(1, "First task."), task(2, "Second task."), {Seq: 3, Agent: "/root/reviewer", Kind: "final", Observed: time.Now(), activitySeq: 7, journal: &journalItem{}}}
	v.blocks = [][]activityui.Block{
		{{Kind: "message", From: "/root", To: "/root/reviewer", Body: "First task."}},
		{{Kind: "message", From: "/root", To: "/root/reviewer", Body: "Second task."}},
		{{Kind: "final", Journal: answer}},
	}
	feed := v.renderFeed(60, 40)
	text := strings.Join(threadPlain(feed), "\n")
	if !strings.Contains(text, "├─→ follow-up") || !strings.Contains(text, "├─✓ finished") || !strings.Contains(text, "↩ re: assignment") {
		t.Fatalf("an answer to the earlier task must keep its quote:\n%s", text)
	}
	v.blocks[2][0].Journal.Groups[0].Target = 2
	v.runs = nil
	if text := strings.Join(threadPlain(v.renderFeed(60, 40)), "\n"); strings.Contains(text, "↩ re:") {
		t.Fatalf("an answer to the nearest task repeated it:\n%s", text)
	}
}

func TestConversationThreadContinuesThroughReasoning(t *testing.T) {
	const width = 60
	v := threadTestView()
	reasoning := activityPaneEntry{Seq: 3, Agent: "Main", Kind: "reasoning", Text: "**Waiting on review**\n\nThe reviewer is still reading.", Observed: v.entries[0].Observed.Add(time.Second)}
	v.entries = []activityPaneEntry{v.entries[0], reasoning, v.entries[1]}
	v.blocks = [][]activityui.Block{v.blocks[0], parseLiveActivity(reasoning), v.blocks[1]}
	feed := v.renderFeed(width, 40)
	plain := threadPlain(feed)
	text := strings.Join(plain, "\n")
	if !strings.Contains(text, "├─← replied · 58s") || !strings.Contains(text, " lines") || strings.Contains(text, "← reviewer") {
		t.Fatalf("reasoning split the thread:\n%s", text)
	}
	for i, row := range plain {
		if ansi.StringWidth(feed.lines[i]) > width {
			t.Fatalf("row %d overflows: %q", i, row)
		}
		if row == "" || !strings.HasPrefix(row, "▶") && !strings.HasPrefix(row, "│") && !strings.HasPrefix(row, "├─") && !strings.HasPrefix(row, "╰─") {
			t.Fatalf("row %d left the thread rail:\n%s", i, text)
		}
		if strings.Contains(row, "still reading") && !strings.HasPrefix(row, "│ ") {
			t.Fatalf("reasoning row lost the rail: %q", row)
		}
	}
	if !strings.Contains(text, "still reading") {
		t.Fatalf("reasoning summary was dropped:\n%s", text)
	}

	// Reasoning after the latest item is outside the thread until it continues.
	v.entries, v.blocks = v.entries[:2], v.blocks[:2]
	v.runs = nil
	pending := strings.Join(threadPlain(v.renderFeed(width, 40)), "\n")
	if strings.Contains(pending, " lines") || strings.Contains(pending, "│ The reviewer") {
		t.Fatalf("trailing reasoning joined an unanswered thread:\n%s", pending)
	}
}
