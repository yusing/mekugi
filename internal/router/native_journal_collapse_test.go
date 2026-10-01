package router

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

// Matches the supplied image transcription: one completed task and three long
// notes, whose two-line previews made the completed report unnecessarily tall.
func nativeJournalCompletedFixture() *nativeJournalCard {
	task := journalItem{Path: "/1", Kind: "task", Title: "Remove Main reply pinning, update rendered coverage, and validate", State: "done",
		Started: &journalStamp{At: journalNoiseTime().Add(-341 * time.Second).Format(time.RFC3339Nano)}, Finished: &journalStamp{At: journalNoiseTime().Format(time.RFC3339Nano)}}
	j := threadJournal{Items: []journalItem{task}, Events: []journalEvent{{Seq: 1, Op: "set", Path: task.Path, Fields: task.node()}}}
	for i, title := range []string{"Removed Main reply pinning", "Rendered coverage passed", "Independent inspection completed"} {
		note := journalNode{Path: fmt.Sprintf("/1/%d", i+1), Kind: "note", Title: "Note",
			Body: title + ". " + strings.Repeat("Supporting evidence remains available in the full report. ", 4) + fmt.Sprintf("DETAIL-%d-END", i+1)}
		j.Events = append(j.Events, journalEvent{Seq: uint64(i + 2), Op: "add", Path: note.Path, Fields: note})
	}
	return &nativeJournalCard{Journal: j}
}

func TestUISnapshotNativeJournalCompletionHeight(t *testing.T) {
	v := newLiveActivityView()
	v.painter.Theme = livediff.DarkTheme
	card := nativeJournalCompletedFixture()
	entry := activityPaneEntry{Seq: 1, Observed: journalNoiseTime(), journalCard: card, native: &liveActivityNativeItem{}}
	var rows []string
	for _, width := range []int{100, 70, 40, 100} {
		var out conversationLines
		v.journalCardLines(&out, entry, width)
		rows = append(rows, fmt.Sprintf("%d columns", width))
		rows = append(rows, out.lines...)
	}
	assertNativeJournalSnapshot(t, "journal-completion-height", rows)
	finished := card.Journal.Items[0].Finished
	card.Journal.Items[0].State = "working"
	card.Journal.Items[0].Finished = nil
	card.Journal.Events[0].Fields.State = "working"
	card.Journal.Events[0].Fields.Finished = nil
	var out conversationLines
	v.journalCardLines(&out, entry, 70)
	assertNativeJournalSnapshot(t, "journal-completion-open", out.lines)
	card.Journal.Items[0].State = "done"
	card.Journal.Items[0].Finished = finished
	card.Journal.Events[0].Fields.State = "done"
	card.Journal.Events[0].Fields.Finished = finished
	card.Journal.mountUnavailable = "Child journal could not be read."
	out = conversationLines{}
	v.journalCardLines(&out, entry, 70)
	assertNativeJournalSnapshot(t, "journal-completion-diagnostic", out.lines)
}

func TestUISnapshotNativeJournalCompletionTaskCountIsNotHeight(t *testing.T) {
	v := newLiveActivityView()
	v.painter.Theme = livediff.DarkTheme
	card := &nativeJournalCard{}
	for i := range 7 {
		node := journalNode{Path: fmt.Sprintf("/%d", i+1), Kind: "task", Title: fmt.Sprintf("Step %d", i+1), State: "done"}
		card.Journal.Events = append(card.Journal.Events, journalEvent{Seq: uint64(i + 1), Op: "set", Path: node.Path, Fields: node})
	}
	var out conversationLines
	v.journalCardLines(&out, activityPaneEntry{Observed: journalNoiseTime(), journalCard: card}, 70)
	assertNativeJournalSnapshot(t, "journal-completion-short-tasks", out.lines)
}

func TestUISnapshotNativeJournalCompletionOpensFullReport(t *testing.T) {
	v := newLiveActivityView()
	v.painter.Theme = livediff.DarkTheme
	card := nativeJournalCompletedFixture()
	beforeItems, beforeEvents := slices.Clone(card.Journal.Items), slices.Clone(card.Journal.Events)
	entry := activityPaneEntry{Seq: 1, Agent: "Main", Kind: "journal_card", Observed: journalNoiseTime(), journalCard: card, native: &liveActivityNativeItem{}}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	var out conversationLines
	v.journalCardLines(&out, entry, 70)
	u := &terminalUI{}
	if len(out.snippets) < 2 || out.snippets[1].run != entry.Seq || !u.openEntry(v, out.snippets[1].run) || u.output == nil {
		t.Fatal("collapsed disclosure lost its report target")
	}
	page := v.painter.DialogPage(u.output.pages[0], 70)
	assertNativeJournalDialogSnapshot(t, "journal-completion-expanded", page)
	for i := range 3 {
		if !strings.Contains(page.Text, fmt.Sprintf("DETAIL-%d-END", i+1)) {
			t.Fatal("expanded report copy omitted full note evidence")
		}
	}
	if !reflect.DeepEqual(beforeItems, card.Journal.Items) || !reflect.DeepEqual(beforeEvents, card.Journal.Events) {
		t.Fatal("collapse changed durable content")
	}
}
