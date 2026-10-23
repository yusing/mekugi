package launch

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/charmbracelet/x/ansi"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// threadTestView is Main with a long assignment and the agent's long reply.
func threadTestView() *activityAdapter {
	start := time.Date(2026, 9, 27, 21, 40, 8, 0, time.Local)
	task := strings.Repeat("Review the follow-scroll changes read-only. ", 12)
	reply := strings.Repeat("Inspection found no behavioral failure. ", 16)
	v := newActivityAdapter()
	v.Conversation, v.FeedOnly = true, true
	v.testAudience()
	v.Entries = []sessionpresentation.ActivityRecord{
		{Source: sessionpresentation.ActivityEntry{Seq: 1, Agent: "Main", Kind: "start", Observed: start, Assignment: &sessionpresentation.ActivityAssignment{To: "/root/reviewer", Text: task}}, Values: activityui.Values([]activityui.Block{{Kind: "start", From: "/root", To: "/root/reviewer", Label: "gpt-6-astra", Body: task}})},
		{Source: sessionpresentation.ActivityEntry{Seq: 2, Agent: "/root/reviewer", Kind: "reply", Observed: start.Add(58 * time.Second), ActivitySeq: 9}, Values: activityui.Values([]activityui.Block{{Kind: "message", From: "/root/reviewer", To: "/root", Body: reply}})},
	}
	return v
}

func threadPlain(feed activityui.Feed) []string {
	plain := make([]string, len(feed.Lines))
	for i, line := range feed.Lines {
		plain[i] = ansi.Strip(line)
	}
	return plain
}

func TestConversationThreadsReplyUnderAssignment(t *testing.T) {
	const width = 60
	v := threadTestView()
	feed := v.RenderFeed(width, 40)
	plain := threadPlain(feed)
	want := []string{"▶ reviewer started · gpt-6-astra", "│ ", "│ ", "├─← replied · 58s", "│ ", "│ ", "│ ", "│ ", "╰─↩ Open reply · +"}
	if len(plain) != len(want) {
		t.Fatalf("thread rows = %d, want %d:\n%s", len(plain), len(want), strings.Join(plain, "\n"))
	}
	for i, prefix := range want {
		if !strings.HasPrefix(plain[i], prefix) || ansi.StringWidth(feed.Lines[i]) > width {
			t.Fatalf("row %d = %q, want prefix %q:\n%s", i, plain[i], prefix, strings.Join(plain, "\n"))
		}
	}
	if !strings.HasSuffix(plain[0], "21:40:08") || strings.Contains(plain[3], "21:4") {
		t.Fatalf("reply should show elapsed time instead of a second clock: %q / %q", plain[0], plain[3])
	}
	if !strings.Contains(plain[2], "… ") || !strings.HasSuffix(plain[2], " lines") {
		t.Fatalf("collapsed assignment lost its ellipsis or count: %q", plain[2])
	}
	if !strings.HasSuffix(plain[7], "…") || feed.Questions[8] != 2 {
		t.Fatalf("reply excerpt or its dialog link is wrong: %q, link %d", plain[7], feed.Questions[8])
	}
	snippet := feed.Snippets[1]
	if snippet != (activityui.Snippet{Run: 1, Block: 0}) || feed.Snippets[2] != snippet || feed.Questions[1] != 0 {
		t.Fatalf("collapsed assignment is not expandable: %+v", feed.Snippets[:3])
	}

	shell := &terminalUI{Attachment: &terminalsession.Attachment{}}
	v.Opening = snippet
	shell.openRequested(v.bind(shell))
	if shell.Dialog == nil || !strings.Contains(shell.Dialog.Pages[0].Body, "Review the follow-scroll") {
		t.Fatal("collapsed assignment did not open full dialog")
	}
	if again := threadPlain(v.RenderFeed(width, 40)); len(again) != len(plain) {
		t.Fatalf("dialog request changed the feed:\n%s", strings.Join(again, "\n"))
	}
}

func TestConversationThreadEndsAtOtherTraffic(t *testing.T) {
	v := threadTestView()
	// Pending: the assignment is the latest item, so it stays in full.
	v.Entries = v.Entries[:1]
	pending := threadPlain(v.RenderFeed(60, 40))
	if strings.Contains(strings.Join(pending, "\n"), "lines") || len(pending) < 5 {
		t.Fatalf("an unanswered assignment was collapsed:\n%s", strings.Join(pending, "\n"))
	}
	// Another agent's traffic in between ends the thread; the reply keeps its
	// own heading, and the closing connector.
	v = threadTestView()
	other := sessionpresentation.ActivityEntry{Seq: 3, Agent: "/root/other", Kind: "reply", Observed: v.Entries[1].Source.Observed, ActivitySeq: 8}
	v.Entries = []sessionpresentation.ActivityRecord{
		v.Entries[0],
		{Source: other, Values: activityui.Values([]activityui.Block{{Kind: "message", From: "/root/other", To: "/root", Body: "Unrelated."}})},
		v.Entries[1],
	}
	text := strings.Join(threadPlain(v.RenderFeed(60, 40)), "\n")
	if strings.Contains(text, "├─") || strings.Contains(text, " lines\n") || !strings.Contains(text, "← reviewer") {
		t.Fatalf("non-adjacent traffic joined a thread:\n%s", text)
	}
}

func TestConversationAsidesFollowThread(t *testing.T) {
	for _, kind := range []string{"reasoning"} {
		t.Run(kind, func(t *testing.T) {
			const width = 60
			v := threadTestView()
			reasoning := sessionpresentation.ActivityEntry{Seq: 3, Agent: "Main", Kind: "reasoning", Text: "**Waiting on review**\n\nThe reviewer is still reading.", Observed: v.Entries[0].Source.Observed.Add(time.Second)}
			if kind != "reasoning" {
				reasoning.Kind, reasoning.Text = "progress", kind+" · reviewer still reading"
				reasoning.Native = &sessionpresentation.NativeItem{Wait: &activitypresentation.Value{Kind: "progress", Body: reasoning.Text}}
			}
			v.appendEntry(reasoning, parseLiveActivity(reasoning))
			v.Model.Reorder([]int{0, 2, 1})
			v.Runs = nil
			feed := v.RenderFeed(width, 40)
			plain := threadPlain(feed)
			text := strings.Join(plain, "\n")
			if !strings.Contains(text, "├─← replied · 58s") || !strings.Contains(text, " lines") || strings.Contains(text, "← reviewer") {
				t.Fatalf("reasoning split the thread:\n%s", text)
			}
			end := slices.IndexFunc(plain, func(row string) bool { return strings.HasPrefix(row, "╰─") })
			reading := slices.IndexFunc(plain, func(row string) bool { return strings.Contains(row, "still reading") })
			if kind != "reasoning" && reading != -1 {
				t.Fatalf("wait leaked into transcript:\n%s", text)
			}
			waiting := slices.IndexFunc(plain, func(row string) bool { return strings.Contains(row, "Waiting on review") })
			if end < 0 || kind == "reasoning" && (waiting < end || reading >= 0 || strings.HasPrefix(plain[waiting], "│")) {
				t.Fatalf("reasoning is not after the thread:\n%s", text)
			}
			for i, row := range plain[:end+1] {
				if ansi.StringWidth(feed.Lines[i]) > width {
					t.Fatalf("row %d overflows: %q", i, row)
				}
				if row == "" || !strings.HasPrefix(row, "▶") && !strings.HasPrefix(row, "│") && !strings.HasPrefix(row, "├─") && !strings.HasPrefix(row, "╰─") {
					t.Fatalf("row %d left the thread rail:\n%s", i, text)
				}
			}

			// Reasoning after the latest item is outside the thread until it continues.
			v.Model.Remove(2, len(v.Entries))
			v.Runs = nil
			pending := strings.Join(threadPlain(v.RenderFeed(width, 40)), "\n")
			if strings.Contains(pending, " lines") || strings.Contains(pending, "│ The reviewer") {
				t.Fatalf("trailing reasoning joined an unanswered thread:\n%s", pending)
			}
		})
	}
}

func TestConversationMainToolsFollowTheirLead(t *testing.T) {
	const width = 70
	at := time.Date(2026, 9, 27, 23, 21, 0, 0, time.Local)
	reasoning := sessionpresentation.ActivityEntry{Seq: 1, Agent: "Main", Kind: "reasoning", Text: "**Preparing regression seed**", Observed: at}
	read := sessionpresentation.ActivityEntry{Seq: 2, Agent: "Main", Kind: "tool", Text: "Read `mchanges.go 470:491`", Observed: at}
	done := sessionpresentation.ActivityEntry{Seq: 3, Agent: "/root/lookup", Kind: "final", Text: "Evidence points to a namespace-context loss.", Observed: at.Add(time.Minute), ActivitySeq: 9}
	edit := sessionpresentation.ActivityEntry{Seq: 4, Agent: "Main", Kind: "tool", Text: "Edit `change_capture_test.go` +65 -0", Observed: at.Add(2 * time.Minute)}
	render := func(entries ...sessionpresentation.ActivityEntry) []string {
		v := newActivityAdapter()
		v.Conversation, v.FeedOnly = true, true
		v.testAudience()
		for _, entry := range entries {
			v.appendEntry(entry, parseLiveActivity(entry))
		}
		return threadPlain(v.RenderFeed(width, 40))
	}
	row := func(plain []string, text string) int {
		return slices.IndexFunc(plain, func(row string) bool { return strings.Contains(row, text) })
	}

	// A lead with no tools yet moves below the traffic that interrupted it.
	plain := render(reasoning, done, edit)
	text := strings.Join(plain, "\n")
	card, seed, tool := row(plain, "lookup"), row(plain, "Preparing regression seed"), row(plain, "Edit")
	if card < 0 || seed < card || tool != seed+1 || strings.Contains(text, "continued") {
		t.Fatalf("Main's tools are not under their reasoning:\n%s", text)
	}

	// A lead that already heads tools stays; the later tools name it.
	plain = render(reasoning, read, done, edit)
	text = strings.Join(plain, "\n")
	card, seed, tool = row(plain, "lookup"), row(plain, "Preparing regression seed"), row(plain, "Edit")
	resumed := row(plain, "continued")
	// The read branches from its reasoning row.
	if seed != 0 || row(plain, "└ Read") != seed+1 || resumed < card || !strings.Contains(plain[resumed], "Preparing regression seed") || tool != resumed+1 {
		t.Fatalf("Main's later tools do not continue their reasoning:\n%s", text)
	}
	for _, line := range plain {
		if ansi.StringWidth(line) > width {
			t.Fatalf("row overflows: %q", line)
		}
	}

	// Tools directly under their lead branch from it.
	if plain := render(reasoning, edit, done); row(plain, "Edit") != 1 || row(plain, "continued") >= 0 {
		t.Fatalf("uninterrupted tools moved:\n%s", strings.Join(plain, "\n"))
	}
}

func TestConversationConsecutiveReasoning(t *testing.T) {
	for _, main := range []bool{true, false} {
		v := newActivityAdapter()
		v.Conversation, v.ChildrenOnly = main, !main
		v.testAudience()
		agent := "Main"
		if !main {
			agent = "/root/worker"
		}
		for i, body := range []string{"**First**\n\nOld body", "**Second**\n\nSecond body", "Latest body"} {
			entry := sessionpresentation.ActivityEntry{Seq: uint64(i + 1), Agent: agent, Kind: "reasoning", Text: body,
				Native: &sessionpresentation.NativeItem{Thread: "thread", Turn: "turn", Item: body, Phase: "item/completed", Thought: 12 * time.Second}}
			v.appendEntry(entry, parseLiveActivity(entry))
		}
		feed := v.RenderFeed(80, 40)
		got := strings.Join(threadPlain(feed), "\n")
		if strings.Count(got, "Thought for 12s • First") != 1 || strings.Count(got, "Thought for 12s • Second") != 1 || strings.Count(got, "Thought for 12s • Latest body") != 1 || strings.Contains(got, "Old body") || strings.Contains(got, "Second body") {
			t.Fatalf("independent folded items: %s", got)
		}
		shell := &terminalUI{Attachment: &terminalsession.Attachment{}}
		opened := 0
		for _, snippet := range feed.Snippets {
			if snippet == (activityui.Snippet{}) {
				continue
			}
			if !shell.openOutput(v.bind(shell), snippet) {
				t.Fatal("reasoning did not request dialog")
			}
			page := v.Painter.DetailDocument(shell.Dialog.Pages[0].DetailPage, nil, nil, 80)
			if page.Text != v.Entries[opened].Source.Text {
				t.Fatalf("wrong item opened: %q", page.Text)
			}
			opened++
		}
		if opened != 2 {
			t.Fatalf("only %d items could reopen", opened)
		}
	}
}

func TestRosterJoinsConsecutiveReasoning(t *testing.T) {
	v := newActivityAdapter()
	v.ChildrenOnly = true
	v.testAudience()
	entries := []sessionpresentation.ActivityEntry{
		{Seq: 1, Agent: "/root/worker", Kind: "reasoning", Text: "**Before action**"},
		{Seq: 2, Agent: "/root/worker", Kind: "tool", Text: "Read `Makefile`"},
		{Seq: 3, Agent: "/root/worker", Kind: "reasoning", Text: "**First**"},
		{Seq: 4, Agent: "/root/other", Kind: "tool", Text: "Read `go.mod`"},
		{Seq: 5, Agent: "/root/worker", Kind: "reasoning", Text: "**Second**"},
	}
	for _, entry := range entries {
		v.appendEntry(entry, parseLiveActivity(entry))
	}
	for _, live := range []bool{false, true} {
		summary, _ := v.testCurrent(sessionpresentation.ActivityAgent{Name: "/root/worker", Responding: live}, time.Now())
		if got := ansi.Strip(summary); got != "First, Second" {
			t.Fatalf("live=%v: inline = %q", live, got)
		}
	}
}
