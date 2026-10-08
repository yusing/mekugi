package router

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
)

func journalNoiseTime() time.Time {
	return time.Date(2026, 9, 30, 22, 31, 0, 0, time.Local)
}

func journalNoiseTask(path, state string, seq uint64) journalItem {
	return journalItem{ID: path, Path: path, Kind: "task", Title: "Task " + path, State: state, Author: "/root", Updated: seq}
}

func TestJournalNoisePinUsesOwnedStateAndLatestUpdate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []journalItem
		want  string
	}{
		{"working beats newer pending", []journalItem{journalNoiseTask("/1", "working", 1), journalNoiseTask("/4", "pending", 4)}, "/1"},
		{"working beats newer done", []journalItem{journalNoiseTask("/1", "working", 1), journalNoiseTask("/4", "done", 4)}, "/1"},
		{"newest working wins", []journalItem{journalNoiseTask("/1", "working", 1), journalNoiseTask("/2", "working", 2)}, "/2"},
		{"blocked beats newer pending", []journalItem{journalNoiseTask("/1", "blocked", 1), journalNoiseTask("/4", "pending", 4)}, "/1"},
		{"newest blocked wins", []journalItem{journalNoiseTask("/1", "blocked", 1), journalNoiseTask("/2", "blocked", 2)}, "/2"},
		{"newest pending wins", []journalItem{journalNoiseTask("/1", "pending", 1), journalNoiseTask("/2", "pending", 2)}, "/2"},
		{"mounted working excluded", []journalItem{journalNoiseTask("/1", "pending", 1), journalNoiseTask("/1/@child/1", "working", 9), journalNoiseTask("/@agents/@child", "working", 10)}, "/1"},
		{"finished hidden when idle", []journalItem{journalNoiseTask("/1", "done", 1), journalNoiseTask("/2", "dropped", 2)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Reverse storage order as well: selection must not depend on list order.
			for _, reverse := range []bool{false, true} {
				items := slices.Clone(tc.items)
				if reverse {
					slices.Reverse(items)
				}
				j := threadJournal{Version: 2, TreeAuthored: true, Items: items}
				for _, item := range tc.items {
					j.Events = append(j.Events, journalEvent{Seq: item.Updated, At: journalNoiseTime().Format(time.RFC3339Nano), Op: "set", Path: item.Path, Fields: item.node(), Transition: true})
				}
				u, _ := newAppServerTestUI()
				u.journal = &nativeJournalSink{tree: &j}
				pin, ok := u.journalPin()
				if ok != (tc.want != "") || pin.node.Path != tc.want {
					t.Fatalf("reverse=%v: pin=%+v, visible=%v, want path %q", reverse, pin, ok, tc.want)
				}
				if ok && pin.total != 1 && strings.Contains(tc.name, "mounted") {
					t.Fatalf("mounted tasks leaked into progress: %+v", pin)
				}
			}
		})
	}
}

func TestUISnapshotJournalNoiseStripPinsWorkingNotLatestEvent(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	j := threadJournal{Version: 2, TreeAuthored: true, Items: []journalItem{journalNoiseTask("/1", "working", 1), journalNoiseTask("/4", "pending", 4)}}
	j.Events = []journalEvent{nativeJournalEvent(4, journalNoiseTime(), "add", "/4", "task", "Task /4", "pending", "")}
	u.journal = &nativeJournalSink{tree: &j}
	assertNativeJournalSnapshot(t, "journal-noise-working-strip", []string{u.journalPlanStrip(100)})
}

func journalNoiseCard() *nativeJournalCard {
	blocked := journalNoiseTask("/1", "blocked", 2)
	blocked.Title = "Check build prerequisites and recovery"
	blocked.Reason = "Required build prerequisites missing: " + strings.Repeat("autoconf automake pkg-config protoc libprotobuf-dev zlib1g-dev libncurses-dev libssl-dev; ", 3) + "REASON-END"
	initial := blocked.node()
	initial.State, initial.Reason = "working", ""
	j := threadJournal{Version: 2, TreeAuthored: true, Author: "/root", Items: []journalItem{blocked}, Events: []journalEvent{
		{Seq: 1, Op: "add", Path: "/1", Fields: initial, Transition: true},
		{Seq: 2, Op: "set", Path: "/1", Fields: blocked.node(), Transition: true},
	}}
	for i := range 5 {
		path := fmt.Sprintf("/1/%d", i+1)
		body := fmt.Sprintf("Evidence %d established. ", i+1) + strings.Repeat("Detailed evidence with retained context. ", 8) + fmt.Sprintf("NOTE-%d-END", i+1)
		note := journalItem{ID: path, Path: path, Kind: "note", Title: fmt.Sprintf("Evidence %d", i+1), Body: body, Updated: uint64(i + 3)}
		j.Items = append(j.Items, note)
		j.Events = append(j.Events, journalEvent{Seq: uint64(i + 3), Op: "add", Path: path, Fields: note.node()})
	}
	// Captured answers must not reappear in either collapsed or expanded reports.
	j.Events = append(j.Events, journalEvent{Seq: 8, Op: "add", Path: "/2", Fields: journalNode{Path: "/2", Kind: "answer", Body: "CAPTURED-ANSWER"}})
	return &nativeJournalCard{Journal: j}
}

func TestUISnapshotJournalNoiseCardPreviewAndExpanded(t *testing.T) {
	v := newLiveActivityView()
	v.passed = map[uint64]bool{1: true}
	v.painter.Theme = livediff.DarkTheme
	card := journalNoiseCard()
	entry := activityPaneEntry{Seq: 1, Observed: journalNoiseTime(), journalCard: card, native: &liveActivityNativeItem{}}
	var out conversationLines
	v.journalCardLines(&out, entry, 70)
	assertNativeJournalSnapshot(t, "journal-noise-card-preview", out.lines)
	rows, _ := journalCardRows(&v.painter, card, 66, true)
	assertNativeJournalSnapshot(t, "journal-noise-card-expanded", rows)
	u, _ := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	u.journal = &nativeJournalSink{tree: &card.Journal}
	assertNativeJournalSnapshot(t, "journal-noise-blocked-strip", []string{u.journalPlanStrip(100)})
}

func TestJournalNoiseCardPreservesDetailAndOpenCount(t *testing.T) {
	v := newLiveActivityView()
	v.painter.Theme = livediff.DarkTheme
	card := journalNoiseCard()
	beforeItems, beforeEvents := slices.Clone(card.Journal.Items), slices.Clone(card.Journal.Events)
	_, facts := journalCardRows(&v.painter, card, 66, false)
	if !slices.Contains(facts, "1 open") {
		t.Fatalf("deduplicating changed tasks lost open count: %v", facts)
	}
	entry := activityPaneEntry{Seq: 1, Observed: journalNoiseTime(), journalCard: card}
	block := v.journalCardBlock(entry)
	for _, text := range []string{block.Body, ansi.Strip(strings.Join(block.Rows(66), "\n"))} {
		if !strings.Contains(text, "REASON-END") || strings.Contains(text, "CAPTURED-ANSWER") {
			t.Fatalf("expanded/copied report lost detail or duplicated answer: %s", text)
		}
		for i := 1; i <= 5; i++ {
			if !strings.Contains(text, fmt.Sprintf("NOTE-%d-END", i)) {
				t.Fatalf("expanded/copied report omitted note %d", i)
			}
		}
	}
	if !reflect.DeepEqual(beforeItems, card.Journal.Items) || !reflect.DeepEqual(beforeEvents, card.Journal.Events) {
		t.Fatal("preview rendering changed durable journal content")
	}
}

func TestUISnapshotJournalNoiseRichNotePreview(t *testing.T) {
	v := newLiveActivityView()
	v.painter.FileLink = func(string) bool { return true }
	v.passed = map[uint64]bool{1: true}
	v.painter.Theme = livediff.DarkTheme
	body := "**Validation passed** with `cat  x | sort`. See [report](</tmp/two  spaces.md>).\n\nFull supporting evidence remains available."
	note := journalNode{Path: "/1", Kind: "note", Title: "Note", Body: body}
	card := &nativeJournalCard{Journal: threadJournal{Events: []journalEvent{{Seq: 1, Op: "add", Path: "/1", Fields: note}}}}
	entry := activityPaneEntry{Seq: 1, Observed: journalNoiseTime(), journalCard: card, native: &liveActivityNativeItem{}}
	var out conversationLines
	v.journalCardLines(&out, entry, 90)
	assertNativeJournalSnapshot(t, "journal-noise-rich-note-preview", out.lines)
	var events conversationLines
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 2, Kind: "journal_event", journalEvent: &card.Journal.Events[0]}}})
	v.journalEventsItem(&events, 0, 0, 40)
	expanded, _ := journalCardRows(&v.painter, card, 86, true)
	pane := (&nativeJournalView{fileLink: v.painter.FileLink}).renderRow(journalPaneRow{node: note}, 90, livediff.DarkTheme)
	for label, rows := range map[string][]string{"pane": {pane}, "events": events.lines, "preview": out.lines, "expanded": expanded} {
		styled := strings.Join(rows, "\n")
		if !strings.Contains(styled, "\x1b]8;;file:///tmp/two%20%20spaces.md") {
			t.Fatalf("%s lost the link target: %q", label, styled)
		}
		screen := vt.NewEmulator(90, len(rows))
		for y, row := range rows {
			fmt.Fprintf(screen, "\x1b[%d;1H%s", y+1, row)
			if x := strings.Index(ansi.Strip(row), "Validation passed"); x >= 0 && screen.CellAt(x, y).Style.Attrs&uv.AttrBold == 0 {
				t.Errorf("%s lost bold result text", label)
			}
		}
		screen.Close()
	}
	first, _, _ := strings.Cut(body, "\n")
	for _, muted := range []journalNode{{Kind: "task", State: "dropped", Title: first}, {Kind: "note", Title: "Note", Body: body, SupersededBy: "/2"}} {
		row := (&nativeJournalView{fileLink: v.painter.FileLink}).renderRow(journalPaneRow{node: muted}, 90, livediff.DarkTheme)
		if !strings.Contains(row, "\x1b]8;;file:///tmp/two%20%20spaces.md") {
			t.Fatalf("dimmed row lost its link target: %q", row)
		}
	}
	assertNativeJournalSnapshot(t, "journal-rich-note-pane-and-events", append([]string{pane}, events.lines...))
	if card.Journal.Events[0].Fields.Body != body {
		t.Fatal("rich preview rewrote the original note")
	}
}

func TestJournalNoiseInlineMainAggregatesButChildKeepsDelta(t *testing.T) {
	card := journalNoiseCard()
	j := card.Journal
	// A previously visible removal remains; add-then-remove is omitted from Main.
	j.Events = append(j.Events,
		journalEvent{Seq: 9, Op: "remove", Path: "/9", Fields: journalNode{Path: "/9", Kind: "task", Title: "Old task", State: "pending"}},
		journalEvent{Seq: 10, Op: "add", Path: "/10", Fields: journalNode{Path: "/10", Kind: "task", Title: "Transient task", State: "pending"}},
		journalEvent{Seq: 11, Op: "remove", Path: "/10", Fields: journalNode{Path: "/10", Kind: "task", Title: "Transient task", State: "pending"}},
	)
	main := journalTurnCard(j, 0, false)
	if strings.Count(main, "Check build prerequisites and recovery") != 1 || strings.Contains(main, "**Remaining**") {
		t.Fatalf("Main repeated a changed/open task: %s", main)
	}
	if !strings.Contains(main, "Removed /9 Old task") || strings.Contains(main, "Transient task") || strings.Contains(main, "CAPTURED-ANSWER") {
		t.Fatalf("Main violated removal/answer exclusions: %s", main)
	}
	child := journalTurnCard(j, 0, true)
	if strings.Count(child, "Check build prerequisites and recovery") != 2 || !strings.Contains(child, "CAPTURED-ANSWER") || !strings.Contains(child, "Transient task") {
		t.Fatalf("Main aggregation changed child event delta: %s", child)
	}
	if later := journalTurnCard(j, 2, true); strings.Count(later, "Check build prerequisites and recovery") != 1 || !strings.Contains(later, "**Remaining**") {
		t.Fatalf("child follow-up lost unchanged open task or repeated earlier events: %s", later)
	}
}
