package router

import (
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestUISnapshotJournalAccepted(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	accepted := journalNoiseTask("/1", "accepted", 1)
	accepted.Title = "Integrate batch"
	j := threadJournal{Version: 2, TreeAuthored: true, Author: "/root", Items: []journalItem{accepted}, Events: []journalEvent{
		{Seq: 1, Op: "set", Path: "/1", Fields: accepted.node(), Transition: true},
	}}
	u.journal = &nativeJournalSink{tree: &j}
	view := &nativeJournalView{}
	view.rebuild(&j)
	rows := view.render(&j, 70, 6, true, false, livediff.DarkTheme)
	u.turn = "active"
	rows = append(rows, "", "Active turn progress", u.journalPlanStrip(70))
	u.turn = ""
	if pin, ok := u.journalPin(); ok {
		t.Fatal("accepted-only idle journal retained its plan strip", pin)
	}
	var out conversationLines
	u.view.journalCardLines(&out, activityPaneEntry{Seq: 1, Observed: journalNoiseTime(), journalCard: &nativeJournalCard{Journal: j}}, 70)
	rows = append(rows, "", "Work report")
	rows = append(rows, out.lines...)
	assertNativeJournalSnapshot(t, "journal-accepted", rows)
}
