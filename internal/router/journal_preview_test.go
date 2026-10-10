package router

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestUISnapshotJournalAnswerPreviews(t *testing.T) {
	v := nativeJournalView{}
	var rows []string
	for _, body := range []string{
		"## Pruning ownership and locks\n\nFull explanation.",
		"**Validation passed** with `go test`.",
		"Read [report](/home/yusing/projects/mekugi/FIXME.md) and [My Report](</tmp/My Project/report.md>).",
		"\n\n## Findings\n\nSupporting evidence.",
		"Plain text answer.",
		"\n\n",
	} {
		node := journalNode{Path: "/1", Kind: "answer", Title: "Answer", Body: body}
		original := node
		for _, width := range []int{100, 30} {
			rows = append(rows, fmt.Sprintf("%d columns", width), v.renderRow(journalPaneRow{node: node}, width, livediff.DarkTheme))
		}
		if !reflect.DeepEqual(node, original) {
			t.Fatal("pane preview changed the original answer")
		}
	}
	assertNativeJournalSnapshot(t, "journal-answer-previews", rows)
}

func TestJournalPreviewReportGatingAndFullDetail(t *testing.T) {
	for _, j := range []threadJournal{
		{Items: []journalItem{journalNoiseTask("/1", "blocked", 1)}},
		{Events: []journalEvent{{Seq: 1, Op: "add", Path: "/1", Fields: journalNode{Kind: "answer", Body: "Answer."}}}},
		{Events: []journalEvent{
			{Seq: 1, Op: "add", Path: "/1", Fields: journalNode{Kind: "task", Title: "Temporary task"}},
			{Seq: 2, Op: "add", Path: "/1/1", Fields: journalNode{Kind: "note", Title: "Temporary note"}},
			{Seq: 3, Op: "remove", Path: "/1", Fields: journalNode{Kind: "task", Title: "Temporary task"}},
		}},
	} {
		if journalHasReport(j, 0) {
			t.Fatalf("unchanged work, answer or removed subtree triggered a report: %+v", j)
		}
	}
	j := threadJournal{Events: []journalEvent{
		{Seq: 1, Op: "set", Path: "/1/1", Fields: journalNode{Kind: "note", Title: "Obsolete"}},
		{Seq: 2, Op: "remove", Path: "/1", Fields: journalNode{Path: "/1", Kind: "task", Title: "Removed task"}},
	}}
	changed, _, _ := journalCardEntries(j, 0)
	if len(changed) != 1 || changed[0].Path != "/1" || !journalHasReport(j, 0) {
		t.Fatalf("removal did not suppress its descendants: %+v", changed)
	}
	card := journalNoiseCard()
	card.Journal.Events[1].Fields.Body = "Task supporting evidence. TASK-BODY-END"
	unchanged := journalNoiseTask("/4", "pending", 1)
	unchanged.Body = "Unchanged open task context. REMAINING-BODY-END"
	card.Journal.Items = append(card.Journal.Items, unchanged)
	card.Journal.Events = append(card.Journal.Events, journalEvent{Seq: 9, Op: "add", Path: "/3", Fields: journalNode{
		Path: "/3", Kind: "note", Title: "Note", Body: "\n\n**Leading blank lines retained**. NOTE-BODY-END",
	}})
	v := newLiveActivityView()
	rows, _ := journalCardRows(&v.painter, card, 66, true, true)
	for _, body := range []string{ansi.Strip(strings.Join(rows, "\n")), journalTurnCard(card.Journal, 0, false)} {
		for _, marker := range []string{"TASK-BODY-END", "NOTE-BODY-END", "REASON-END", "NOTE-1-END", "REMAINING-BODY-END"} {
			if !strings.Contains(body, marker) {
				t.Fatalf("full report lost %s: %s", marker, body)
			}
		}
	}
}

func TestJournalCompactReportParityAtDelivery(t *testing.T) {
	var reference string
	for _, native := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("native=%t/stream=%t", native, stream), func(t *testing.T) {
				transform, proxy, _, workspace := newDurableTreeTransform(t)
				defer transform.Close()
				thread := transform.shellThreadID
				paths, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{
					{Op: "add", Kind: "task", Title: new("Build prerequisites"), State: new("blocked"), Reason: new(strings.Repeat("Missing dependencies. ", 20) + "FULL-REASON-END")},
				})
				if err != nil {
					t.Fatal(err)
				}
				var notes []journalMutation
				for i := range 5 {
					notes = append(notes, journalMutation{Op: "log", P: paths[0], Text: new(fmt.Sprintf("Fact-%d.\n\n%sFULL-EVIDENCE-%d-END", i, strings.Repeat("Supporting evidence. ", 20), i))})
				}
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", notes); err != nil {
					t.Fatal(err)
				}
				var sink *nativeJournalSink
				if native {
					sink = proxy.journals.attachNative(workspace, thread)
					defer proxy.journals.detachNative(sink)
				}
				messages, frames := separateReportFinal(t, transform, stream, "compact-parity", "Done.")
				// Native transports the stock final unchanged; its UI hides the empty outcome.
				separateReportPreservedAnswer(t, messages, "compact-parity", "Done.", native || stream)
				if native && !sink.hides("raw-compact-parity") {
					t.Fatal("native did not replace the empty outcome with its report")
				}
				separateReportDeliver(transform, frames)
				var report string
				if native {
					for _, publication := range sink.snapshot() {
						if publication.card != nil {
							report = journalMainCard(publication.card.Journal, publication.card.Since, false)
						}
					}
				} else {
					for _, message := range messages {
						if strings.HasPrefix(commentaryMessageText(message), "Journal") {
							report = commentaryMessageText(message)
						}
					}
				}
				if strings.Count(report, "Build prerequisites") != 1 || strings.Contains(report, "**Remaining**") || strings.Contains(report, "FULL-REASON-END") || strings.Contains(report, "Fact-0") || strings.Contains(report, "FULL-EVIDENCE") || !strings.Contains(report, "Fact-4") {
					t.Fatalf("compact delivery repeated work, lost results or expanded supporting evidence: %s", report)
				}
				if reference == "" {
					reference = report
				} else if report != reference {
					t.Fatalf("native/inline or JSON/SSE preview meaning diverged:\n%s\n%s", reference, report)
				}
				if native {
					if j := separateReportRead(t, proxy, workspace, thread); j.FlushSeq != 0 {
						t.Fatal("native publication acknowledged the card before presentation")
					}
					if err := sink.acknowledge(t.Context(), proxy, sink.snapshot()); err != nil {
						t.Fatal(err)
					}
				}
				if j := separateReportRead(t, proxy, workspace, thread); j.FlushSeq != j.Sequence {
					t.Fatalf("compact delivery failed to acknowledge its exact window: %+v", j)
				}
			})
		}
	}
}

func TestJournalPinUsesSelectedTaskTimestamp(t *testing.T) {
	u, _ := newAppServerTestUI()
	at := journalNoiseTime()
	working := journalNoiseTask("/1", "working", 1)
	working.UpdatedAt = at.Format(time.RFC3339Nano)
	pending := journalNoiseTask("/4", "pending", 4)
	pending.UpdatedAt = at.Add(time.Hour).Format(time.RFC3339Nano)
	j := threadJournal{Items: []journalItem{working, pending}, Events: []journalEvent{
		nativeJournalEvent(4, at.Add(time.Hour), "add", "/4", "task", pending.Title, "pending", ""),
	}}
	u.journal = &nativeJournalSink{tree: &j}
	pin, ok := u.journalPin()
	if !ok || pin.node.Path != "/1" || pin.node.Updated.At != working.UpdatedAt {
		t.Fatalf("pin borrowed a newer unrelated event timestamp: %+v, %v", pin, ok)
	}
	u.turn = "active"
	j.Items[0].State, j.Items[1].State = "done", "dropped"
	pin, ok = u.journalPin()
	if !ok || pin.node.Path != "/4" || pin.done != 2 || pin.node.Updated.At != pending.UpdatedAt {
		t.Fatalf("active all-finished tree lost latest finished task: %+v, %v", pin, ok)
	}
	j.Items = nil
	if _, ok := u.journalPin(); ok {
		t.Fatal("removed task survived as a historical-event pin")
	}
}

func TestUISnapshotJournalNoiseNarrowCardAndStrip(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	card := journalNoiseCard()
	u.journal = &nativeJournalSink{tree: &card.Journal}
	var rows []string
	for _, width := range []int{40, 17} {
		rows = append(rows, fmt.Sprintf("%d columns", width), u.journalPlanStrip(width))
	}
	var out conversationLines
	u.view.journalCardLines(&out, activityPaneEntry{Seq: 1, Observed: journalNoiseTime(), journalCard: card, native: &liveActivityNativeItem{}}, 40)
	rows = append(rows, out.lines...)
	assertNativeJournalSnapshot(t, "journal-noise-narrow-card-and-strip", rows)
}

func TestUISnapshotJournalCompletedWorkResults(t *testing.T) {
	v := newLiveActivityView()
	v.painter.Theme = livediff.DarkTheme
	j := threadJournal{}
	for i, title := range []string{"Repair replay selection", "Validate journal presentation"} {
		item := journalNoiseTask(fmt.Sprintf("/%d", i+1), "done", uint64(i+1))
		item.Title = title
		j.Items = append(j.Items, item)
		j.Events = append(j.Events, journalEvent{Seq: uint64(i + 1), Op: "set", Path: item.Path, Fields: item.node()})
	}
	for i, body := range []string{
		"Replay metadata identifies the owning workspace.",
		"Retained answers survive restart without rerunning tools.",
		"Changed open tasks appear once, while the open count remains accurate.",
		"Focused transport and snapshot checks passed.\n\nSupporting command evidence remains available.",
		"Independent review found no remaining correctness issue.\n\nReview scope and details remain available.",
		"Live model compliance is still unmeasured.\n\nOffline rendering cannot establish whether the model follows the revised prompt.",
	} {
		node := journalNode{Path: fmt.Sprintf("/2/%d", i+1), Kind: "note", Title: "Note", Body: body}
		j.Events = append(j.Events, journalEvent{Seq: uint64(i + 3), Op: "add", Path: node.Path, Fields: node})
	}
	entry := activityPaneEntry{Seq: 1, Observed: journalNoiseTime(), journalCard: &nativeJournalCard{Journal: j}, native: &liveActivityNativeItem{}}
	var rows []string
	for _, width := range []int{90, 40} {
		var out conversationLines
		v.journalCardLines(&out, entry, width)
		rows = append(rows, fmt.Sprintf("%d columns, sample content", width))
		rows = append(rows, out.lines...)
	}
	assertNativeJournalSnapshot(t, "journal-completed-work-results", rows)
}
