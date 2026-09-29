package router

import (
	"slices"
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
	if snippet != (liveActivitySnippet{run: 1, block: 0}) || feed.snippets[2] != snippet || feed.questions[1] != 0 {
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

func TestConversationAsidesFollowThread(t *testing.T) {
	for _, kind := range []string{"reasoning", "Waiting for agent", "Finished waiting", "Wait failed"} {
		t.Run(kind, func(t *testing.T) {
			const width = 60
			v := threadTestView()
			reasoning := activityPaneEntry{Seq: 3, Agent: "Main", Kind: "reasoning", Text: "**Waiting on review**\n\nThe reviewer is still reading.", Observed: v.entries[0].Observed.Add(time.Second)}
			if kind != "reasoning" {
				reasoning.Kind, reasoning.Text = "progress", kind+" · reviewer still reading"
				reasoning.native = &liveActivityNativeItem{wait: &activityui.Block{Kind: "progress", Body: reasoning.Text}}
			}
			v.entries = []activityPaneEntry{v.entries[0], reasoning, v.entries[1]}
			v.blocks = [][]activityui.Block{v.blocks[0], parseLiveActivity(reasoning), v.blocks[1]}
			feed := v.renderFeed(width, 40)
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
			if end < 0 || kind == "reasoning" && (reading < end || strings.HasPrefix(plain[reading], "│")) {
				t.Fatalf("reasoning is not after the thread:\n%s", text)
			}
			for i, row := range plain[:end+1] {
				if ansi.StringWidth(feed.lines[i]) > width {
					t.Fatalf("row %d overflows: %q", i, row)
				}
				if row == "" || !strings.HasPrefix(row, "▶") && !strings.HasPrefix(row, "│") && !strings.HasPrefix(row, "├─") && !strings.HasPrefix(row, "╰─") {
					t.Fatalf("row %d left the thread rail:\n%s", i, text)
				}
			}

			// Reasoning after the latest item is outside the thread until it continues.
			v.entries, v.blocks = v.entries[:2], v.blocks[:2]
			v.runs = nil
			pending := strings.Join(threadPlain(v.renderFeed(width, 40)), "\n")
			if strings.Contains(pending, " lines") || strings.Contains(pending, "│ The reviewer") {
				t.Fatalf("trailing reasoning joined an unanswered thread:\n%s", pending)
			}
		})
	}
}

func TestConversationMainToolsFollowTheirLead(t *testing.T) {
	const width = 70
	at := time.Date(2026, 9, 27, 23, 21, 0, 0, time.Local)
	reasoning := activityPaneEntry{Seq: 1, Agent: "Main", Kind: "reasoning", Text: "**Preparing regression seed**", Observed: at}
	read := activityPaneEntry{Seq: 2, Agent: "Main", Kind: "tool", Text: "Read `mchanges.go 470:491`", Observed: at}
	done := activityPaneEntry{Seq: 3, Agent: "/root/lookup", Kind: "final", Text: "Evidence points to a namespace-context loss.", Observed: at.Add(time.Minute), activitySeq: 9}
	edit := activityPaneEntry{Seq: 4, Agent: "Main", Kind: "tool", Text: "Edit `journal_child_changes_test.go` +65 -0", Observed: at.Add(2 * time.Minute)}
	render := func(entries ...activityPaneEntry) []string {
		v := newLiveActivityView()
		v.conversation, v.feedOnly = true, true
		v.entries = entries
		for _, entry := range entries {
			v.blocks = append(v.blocks, parseLiveActivity(entry))
		}
		return threadPlain(v.renderFeed(width, 40))
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
	v := newLiveActivityView()
	v.conversation = true
	v.entries = []activityPaneEntry{
		{Seq: 1, Agent: "Main", Kind: "reasoning", Text: "**First**\n\nOld body\nOld tail"},
		{Seq: 2, Agent: "Main", Kind: "reasoning", Text: "**Second**"},
		{Seq: 3, Agent: "Main", Kind: "reasoning", Text: "**Latest**\n\nCurrent body"},
		{Seq: 4, Agent: "Main", Kind: "tool", Text: "Read `Makefile`"},
		{Seq: 5, Agent: "Main", Kind: "reasoning", Text: "**After action**\n\nSeparate body"},
	}
	for _, entry := range v.entries {
		v.blocks = append(v.blocks, parseLiveActivity(entry))
	}
	feed := v.renderFeed(80, 40)
	got := strings.Join(threadPlain(feed), "\n")
	if !strings.Contains(got, "Old tail, Second") || strings.Contains(got, "Old body") || !strings.Contains(got, "Current body\n└ Read") || !strings.Contains(got, "Separate body") {
		t.Fatalf("collapsed run:\n%s", got)
	}
	snippet := feed.snippets[0]
	if snippet == (liveActivitySnippet{}) {
		t.Fatal("collapsed summaries cannot open")
	}
	v.toggleSnippet(snippet)
	got = strings.Join(threadPlain(v.renderFeed(80, 40)), "\n")
	if !strings.Contains(got, "Old body") {
		t.Fatalf("summary bodies lost:\n%s", got)
	}
	// The Activity view applies the same policy, independently of Main grouping.
	v.conversation, v.childrenOnly, v.runs = false, true, nil
	for i := range v.entries {
		v.entries[i].Agent = "/root/worker"
	}
	v.expanded = nil
	got = strings.Join(threadPlain(v.renderFeed(80, 60)), "\n")
	if !strings.Contains(got, "Old tail, Second") || strings.Contains(got, "Old body") || !strings.Contains(got, "Current body") {
		t.Fatalf("activity run:\n%s", got)
	}
}

func TestRosterJoinsConsecutiveReasoning(t *testing.T) {
	v := newLiveActivityView()
	v.childrenOnly = true
	v.entries = []activityPaneEntry{
		{Seq: 1, Agent: "/root/worker", Kind: "reasoning", Text: "**Before action**"},
		{Seq: 2, Agent: "/root/worker", Kind: "tool", Text: "Read `Makefile`"},
		{Seq: 3, Agent: "/root/worker", Kind: "reasoning", Text: "**First**"},
		{Seq: 4, Agent: "/root/other", Kind: "tool", Text: "Read `go.mod`"},
		{Seq: 5, Agent: "/root/worker", Kind: "reasoning", Text: "**Second**"},
	}
	for _, entry := range v.entries {
		v.blocks = append(v.blocks, parseLiveActivity(entry))
	}
	for _, live := range []bool{false, true} {
		summary, _ := v.current(activityPaneAgent{Name: "/root/worker", Responding: live}, time.Now())
		if got := ansi.Strip(summary); got != "First, Second" {
			t.Fatalf("live=%v: inline = %q", live, got)
		}
	}
}
