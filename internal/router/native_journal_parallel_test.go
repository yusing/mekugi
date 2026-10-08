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

func parallelJournalView(t *testing.T, conversation bool, legacy bool) *liveActivityView {
	t.Helper()
	v := newLiveActivityView()
	v.conversation = conversation
	v.painter.Theme = livediff.DarkTheme
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	v.clock = func() time.Time { return now }
	if legacy {
		v.applyJournal("main", nativeJournalPublication{item: journalItem{ID: "note", Text: "Validation passed with the same rich journal rendering in both panes."}})
		return v
	}
	events := []journalEvent{
		nativeJournalEvent(1, now, "set", "/1", "task", "Unify journal rendering", "working", "Full task details"),
		nativeJournalEvent(2, now, "add", "/1/1", "note", "Note", "", "Validation passed with shared rendering.\n\n**Copy retains this detail.**"),
		nativeJournalEvent(3, now, "set", "/2", "task", "Provider check", "blocked", "Full blocked task details"),
	}
	events[2].Fields.Reason = "Waiting for provider evidence"
	for _, event := range events {
		e := event
		v.applyTreeJournal("main", nativeJournalPublication{event: &e, item: journalItem{ID: fmt.Sprintf("event:%d", e.Seq), Text: journalRowText(e)}})
	}
	card := &nativeJournalCard{Journal: threadJournal{Events: events, Items: []journalItem{{Path: "/3", ID: "/3", Kind: "task", Title: "Finish review", State: "pending"}}}}
	v.applyTreeJournal("main", nativeJournalPublication{card: card, item: journalItem{ID: "card"}, terminal: true})
	return v
}

func TestUISnapshotNativeParallelJournal(t *testing.T) {
	for _, width := range []int{36, 80} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/legacy=%v", width, legacy), func(t *testing.T) {
				main, activity := parallelJournalView(t, true, legacy), parallelJournalView(t, false, legacy)
				left, right := main.renderFeed(width, 80), activity.renderFeed(width, 80)
				assertNativeJournalSnapshot(t, fmt.Sprintf("journal-parallel-%d-legacy-%v", width, legacy), right.lines)
				if !reflect.DeepEqual(left.lines, right.lines) || !reflect.DeepEqual(left.snippets, right.snippets) || !reflect.DeepEqual(left.questions, right.questions) {
					t.Fatal("Main and Activity journal content or click/navigation targets differ")
				}
			})
		}
	}
}

func TestNativeParallelJournalDetailsAndNavigation(t *testing.T) {
	for _, conversation := range []bool{true, false} {
		v := parallelJournalView(t, conversation, false)
		before := len(v.entries)
		feed := v.renderFeed(50, 80)
		for _, seq := range []uint64{1, 2, 3, 4} {
			if _, ok := v.questionRows[seq]; !ok {
				t.Fatalf("journal entry %d lost its pane navigation target", seq)
			}
		}
		var event, card liveActivitySnippet
		for _, snippet := range feed.snippets {
			if snippet.run == 1 {
				event = snippet
			}
			if snippet.run == 4 {
				card = snippet
			}
		}
		u := &terminalUI{}
		if event.run == 0 || !u.openOutput(v, event) || u.output == nil {
			t.Fatal("journal event detail target did not open")
		}
		page := v.painter.DialogPage(u.output.pages[0], 50)
		if !strings.Contains(page.Text, "Full blocked task details") {
			t.Fatalf("event detail/copy lost its body: %q", page.Text)
		}
		if card.run == 0 || !u.openOutput(v, card) {
			t.Fatal("journal work report target did not open")
		}
		page = v.painter.DialogPage(u.output.pages[0], 50)
		if !strings.Contains(page.Text, "Copy retains this detail.") {
			t.Fatalf("work report copy lost full notes: %q", page.Text)
		}
		if len(v.entries) != before || v.following != true {
			t.Fatal("opening journal details mutated retention or pane follow state")
		}
	}
}

func TestNativeParallelJournalFoldsAboveViewport(t *testing.T) {
	for _, conversation := range []bool{true, false} {
		v := parallelJournalView(t, conversation, false)
		for i := range 20 {
			entry := activityPaneEntry{Seq: uint64(i + 5), Agent: "Main", Kind: "text", Text: fmt.Sprintf("Later message %d", i), Observed: v.now()}
			v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
		}
		feed := v.renderFeed(60, 6)
		if !strings.Contains(strings.Join(feed.lines, "\n"), "Copy retains this detail.") || len(feed.passing) != 2 {
			t.Fatal("journal event and report did not start expanded")
		}
		v.following, v.offset = false, feed.passing[0].end-1
		v.viewport(feed, 6)
		if v.passed[3] || v.passed[4] {
			t.Fatal("partially visible journal folded")
		}
		v.offset = feed.passing[1].end + 2
		top := v.viewport(feed, 6)[0]
		feed = v.renderFeed(60, 6)
		if !v.passed[3] || !v.passed[4] || strings.Contains(strings.Join(feed.lines, "\n"), "Copy retains this detail.") {
			t.Fatal("out-of-view journal stayed expanded")
		}
		if got := v.viewport(feed, 6)[0]; ansi.Strip(got) != ansi.Strip(top) {
			t.Fatalf("journal collapse moved the scrolled viewport: conversation=%v top=%q want=%q offset=%d", conversation, got, top, v.offset)
		}
	}
}

func TestNativeParallelLegacyJournalFullyShown(t *testing.T) {
	for _, conversation := range []bool{true, false} {
		for _, terminal := range []bool{true, false} {
			v := newLiveActivityView()
			v.conversation = conversation
			for i, body := range []string{"Milestone details", "Second milestone body"} {
				v.applyJournal("main", nativeJournalPublication{item: journalItem{ID: fmt.Sprint(i), Text: body}, terminal: terminal, batch: 1})
			}
			feed := v.renderFeed(60, 80)
			for _, snippet := range feed.snippets {
				if snippet.run != 0 {
					t.Fatalf("fully shown milestone has redundant dialog: %+v", snippet)
				}
			}
			if _, ok := v.questionRows[1]; !ok {
				t.Fatal("journal lost navigation target")
			}
		}
	}
}
