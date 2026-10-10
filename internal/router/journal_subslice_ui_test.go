package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestUISnapshotJournalNextSubslice(t *testing.T) {
	j := threadJournal{Version: 2, TreeAuthored: true, SliceParents: map[string]bool{"/2": true}, Items: []journalItem{
		journalNoiseTask("/2", "working", 20), journalNoiseTask("/2/8", "done", 8),
		journalNoiseTask("/2/9", "pending", 9), journalNoiseTask("/2/10", "pending", 10),
		journalNoiseTask("/2/11", "pending", 11), journalNoiseTask("/2/@child/1", "working", 30),
	}}
	j.Events = []journalEvent{nativeJournalEvent(21, journalNoiseTime(), "add", "/2/12", "note", "Focused checks passed", "", "")}
	card := &nativeJournalCard{Journal: j, Since: 20}
	v := newLiveActivityView()
	v.painter.Theme = livediff.DarkTheme
	entry := activityPaneEntry{Seq: 1, Observed: journalNoiseTime(), journalCard: card, native: &liveActivityNativeItem{}}
	for mode := range uint8(3) {
		v.expansion = mode
		for _, passed := range []bool{false, true} {
			v.passed = map[uint64]bool{1: passed}
			var out conversationLines
			v.journalCardLines(&out, entry, 90)
			name := "journal-next-subslice-remaining"
			if v.excerpt(passed) {
				name += "-preview"
			}
			assertNativeJournalSnapshot(t, name, out.lines)
		}
	}
	u, _ := newAppServerTestUI()
	u.turn, u.journal = "auto-continue", &nativeJournalSink{tree: &j}
	u.view.painter.Theme = livediff.DarkTheme
	assertNativeJournalSnapshot(t, "journal-next-subslice-composer", []string{u.journalPlanStrip(100)})
	// A working child remains active even when the parent has a newer update.
	j.Items[3].State = "working"
	if pin, _ := u.journalPin(); pin.node.Path != "/2/10" {
		t.Fatalf("active subslice lost to parent: %+v", pin)
	}
	block := v.journalCardBlock(entry)
	for _, text := range []string{block.Body, ansi.Strip(strings.Join(block.Rows(86), "\n"))} {
		for _, title := range []string{"Task /2/9", "Task /2/10", "Task /2/11"} {
			if !strings.Contains(text, title) {
				t.Fatalf("full report lost %q: %s", title, text)
			}
		}
	}
	// A changed active child appears in This turn, not twice in Remaining.
	j.Items[2].State, j.Items[3].State, j.Items[4].State = "working", "pending", "working"
	j.Events = append(j.Events, journalEvent{Seq: 22, Op: "set", Path: "/2/9", Fields: j.Items[2].node()})
	_, left, _ := journalCardEntries(j, 20)
	if next, _, _ := journalRemainingPreview(j, left); next.Path != "/2/11" {
		t.Fatalf("unchanged active task lost to pending sibling: %+v", next)
	}
}
