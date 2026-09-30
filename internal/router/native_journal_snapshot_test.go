package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func assertNativeJournalSnapshot(t *testing.T, name string, rows []string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		uisnapshot.Assert(t, "testdata/snapshots/"+name+".txt", strings.Join(rows, "\n")+"\n")
	})
}

func assertNativeJournalDialogSnapshot(t *testing.T, name string, page activityui.DialogPage) {
	t.Helper()
	var rows []string
	if page.Detail != "" {
		rows = append(rows, page.Detail, "")
	}
	for _, line := range page.Lines {
		rows = append(rows, line.Text)
	}
	assertNativeJournalSnapshot(t, name, rows)
}

func TestUISnapshotJournalReply(t *testing.T) {
	// Fix the renderer's theme and wall-clock inputs, not its rendered output.
	v := newLiveActivityView()
	v.painter.Theme = livediff.DarkTheme
	question := activityPaneEntry{Seq: 7, Agent: "You", Kind: "text", Text: "What was the journal issue?", Observed: time.Date(2026, 9, 30, 8, 16, 39, 0, time.Local)}
	answer := "I tried to bind an agent to a journal note, but agent bindings belong to tasks. The whole batch was rejected, with no partial updates.\n\nI later recorded the progress successfully. It affected only task bookkeeping, not the code, image, or tests."
	entry := activityPaneEntry{Seq: 8, Agent: "Main", Kind: "text", Text: answer, Observed: question.Observed, native: &liveActivityNativeItem{question: question.Seq}}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{question, entry}})
	var card *nativeJournalCard
	var snapshot strings.Builder
	snapshot.WriteString("Actual Main answer and separate journal renderer, ANSI colors removed. Sample content; not a live journal snapshot.\n\n")
	render := func(label string, width int) {
		var out conversationLines
		if card != nil {
			v.journalCardLines(&out, activityPaneEntry{Seq: 9, Observed: question.Observed, journalCard: card}, width)
			out.add(0, "")
		}
		out.lines = append(out.lines, v.conversationItem(1, 1, width, conversationThread{}).lines...)
		snapshot.WriteString(label + "\n\n" + strings.Join(out.lines, "\n") + "\n\n")
	}
	render("SCREENSHOT REPLY, 100 COLUMNS", 100)
	v.entries[0].Text = "Is the work still running?"
	v.entries[1].Text = "Yes. The parser task is still running."
	v.blocks[1] = parseLiveActivity(v.entries[1])
	render("STATUS REPLY WITH UNCHANGED OPEN TASKS, 100 COLUMNS", 100)
	v.entries[0].Text = "Journal UI improvements"
	card = &nativeJournalCard{Journal: threadJournal{Events: []journalEvent{
		{Seq: 1, Op: "add", Path: "/1", Fields: journalNode{Path: "/1", Kind: "task", State: "done", Title: "Integrate reply context and slice navigation"}},
		{Seq: 2, Op: "add", Path: "/2", Fields: journalNode{Path: "/2", Kind: "task", State: "done", Title: "Expand entries and fit older subtrees"}},
		{Seq: 3, Op: "add", Path: "/3", Fields: journalNode{Path: "/3", Kind: "note", Title: "Layout validated"}},
		{Seq: 4, Op: "add", Path: "/4", Fields: journalNode{Path: "/4", Kind: "answer", Body: "Ordinary replies stay conversational. Work reports appear separately."}},
	}}}
	v.entries[1].Text = "Ordinary replies stay conversational. Work reports appear separately."
	v.blocks[1] = parseLiveActivity(v.entries[1])
	render("REPLY WITH JOURNAL ROWS, 100 COLUMNS", 100)
	render("SAME REPLY, 60 COLUMNS", 60)
	v.entries[1].Text = ""
	v.blocks[1] = parseLiveActivity(v.entries[1])
	var empty conversationLines
	v.journalCardLines(&empty, activityPaneEntry{Seq: 9, Observed: question.Observed, journalCard: card}, 60)
	snapshot.WriteString("EMPTY OUTCOME WITH WORK REPORT, 60 COLUMNS\n\n" + strings.Join(empty.lines, "\n") + "\n\n")
	uisnapshot.Assert(t, "testdata/snapshots/journal-ui-preview.txt", snapshot.String())
}
