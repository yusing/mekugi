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
	v.entries = []activityPaneEntry{question}
	answer := "I used agent in a journal add operation, but agent binding is supported only by set. The whole batch was rejected, with no partial updates.\n\nI later recorded the progress successfully. It affected only task bookkeeping, not the code, image, or tests."
	card := &nativeJournalCard{Journal: threadJournal{Events: []journalEvent{
		{Seq: 1, Op: "add", Path: "/1", Fields: journalNode{Path: "/1", Kind: "answer", Body: answer}},
	}}}
	entry := activityPaneEntry{Seq: 8, Observed: question.Observed, journalCard: card, native: &liveActivityNativeItem{question: question.Seq}}
	var snapshot strings.Builder
	snapshot.WriteString("Actual Main journal renderer, ANSI colors removed. Sample content; not a live journal snapshot.\n\n")
	render := func(label string, width int) {
		var out conversationLines
		v.journalCardLines(&out, entry, width)
		snapshot.WriteString(label + "\n\n" + strings.Join(out.lines, "\n") + "\n\n")
	}
	render("SCREENSHOT REPLY, 100 COLUMNS", 100)
	v.entries[0].Text = "Journal UI improvements"
	card.Journal.Events = []journalEvent{
		{Seq: 1, Op: "add", Path: "/1", Fields: journalNode{Path: "/1", Kind: "task", State: "done", Title: "Integrate reply context and slice navigation"}},
		{Seq: 2, Op: "add", Path: "/2", Fields: journalNode{Path: "/2", Kind: "task", State: "done", Title: "Expand entries and fit older subtrees"}},
		{Seq: 3, Op: "add", Path: "/3", Fields: journalNode{Path: "/3", Kind: "note", Title: "Layout validated"}},
		{Seq: 4, Op: "add", Path: "/4", Fields: journalNode{Path: "/4", Kind: "answer", Body: "Reply context now sits inside the journal card. Slice details share one segmented dialog with item scrolling and flash."}},
	}
	render("REPLY WITH JOURNAL ROWS, 100 COLUMNS", 100)
	render("SAME REPLY, 60 COLUMNS", 60)
	uisnapshot.Assert(t, "testdata/snapshots/journal-ui-preview.txt", snapshot.String())
}
