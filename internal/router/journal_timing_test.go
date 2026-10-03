package router

import (
	"github.com/yusing/mekugi/internal/uisnapshot"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestJournalActiveWorkTiming(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proxy, workspace := treeTestJournal(t)
		treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Work"), State: new("working")})
		setState := func(state string) {
			t.Helper()
			m := journalMutation{Op: "set", P: "/1", State: &state}
			if state == "blocked" {
				m.Reason = new("waiting")
			}
			treeApply(t, proxy, workspace, m)
		}
		lifecycle := func(state string) {
			t.Helper()
			if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "tree", state, ""); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(2 * time.Second)
		setState("blocked")
		time.Sleep(time.Hour)
		setState("working")
		time.Sleep(3 * time.Second)
		lifecycle("blocked")
		time.Sleep(time.Hour)
		j := treeSnapshot(t, proxy, workspace)
		if got := j.Items[0].WorkTimer.at(time.Now()); got != 5*time.Second {
			t.Fatalf("paused total = %v", got)
		}
		lifecycle("working")
		time.Sleep(4 * time.Second)
		setState("done")
		j = treeSnapshot(t, proxy, workspace)
		if got := journalTaskElapsed(j.Items[0].node()); got != "9s" {
			t.Fatalf("completed total = %q", got)
		}
		var replay threadJournal
		for _, event := range j.Events {
			applyJournalReplayEvent(&replay, event)
		}
		if got := journalTaskElapsed(replay.Items[0].node()); got != "9s" {
			t.Fatalf("replayed total = %q", got)
		}
		time.Sleep(time.Hour)
		setState("working")
		time.Sleep(2 * time.Second)
		setState("done")
		if got := journalTaskElapsed(treeSnapshot(t, proxy, workspace).Items[0].node()); got != "11s" {
			t.Fatalf("reopened total = %q", got)
		}
	})
}

func TestJournalTimingRestartAndFork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proxy, workspace := treeTestJournal(t)
		treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Work"), State: new("working")})
		time.Sleep(3 * time.Second)
		treeApply(t, proxy, workspace, journalMutation{Op: "log", Text: new("Checkpoint")})
		j := treeSnapshot(t, proxy, workspace)
		j.TimerOwner = "previous-router-process"
		if err := writeThreadJournal(proxy.replayStore, j); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Hour)
		j = treeSnapshot(t, proxy, workspace)
		if got := j.Items[0].WorkTimer.at(time.Now()); got != 3*time.Second {
			t.Fatalf("restart revived clock: %v", got)
		}
		if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, "tree", "resumed"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		if err := proxy.journals.stopJournalTurn(t.Context(), proxy.replayStore, workspace, "tree", "resumed"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Hour)
		if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "fork", "/root", "tree"); err != nil {
			t.Fatal(err)
		}
		fork, exists, err := readThreadJournal(proxy.replayStore, workspace, "fork")
		if err != nil || !exists {
			t.Fatalf("fork: %v %v", exists, err)
		}
		if got := fork.Items[0].WorkTimer.at(time.Now()); got != 5*time.Second || !fork.Items[0].WorkTimer.Since.IsZero() {
			t.Fatalf("fork did not preserve stopped total: %+v", fork.Items[0].WorkTimer)
		}
		if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, "fork", "fork-turn"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "fork", "", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err != nil {
			t.Fatal(err)
		}
		fork, _, _ = readThreadJournal(proxy.replayStore, workspace, "fork")
		if got := journalTaskElapsed(fork.Items[0].node()); got != "6s" {
			t.Fatalf("resumed fork = %q", got)
		}
		if got := treeSnapshot(t, proxy, workspace).Items[0].WorkTimer.at(time.Now()); got != 5*time.Second {
			t.Fatalf("fork changed source: %v", got)
		}
	})
}

func TestJournalLegacyElapsedUnknown(t *testing.T) {
	node := journalNode{Started: &journalStamp{At: "2026-01-01T00:00:00Z"}, Finished: &journalStamp{At: "2026-01-01T01:00:00Z"}}
	if got := journalTaskElapsed(node); got != "" {
		t.Fatalf("wall time claimed as active work: %q", got)
	}
}

func TestUISnapshotJournalActiveWorkTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proxy, workspace := treeTestJournal(t)
		u, _ := newAppServerTestUI()
		u.clock = time.Now
		u.journal = proxy.journals.attachNative(workspace, "tree")
		treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Active work"), State: new("working")})
		var out strings.Builder
		render := func(label string) { out.WriteString(label + "\n" + u.journalPlanStrip(80) + "\n") }
		time.Sleep(2 * time.Second)
		render("Working")
		if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "tree", "blocked", "Interrupted"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Hour)
		render("Interrupted, one hour later")
		if err := proxy.journals.observeLifecycle(t.Context(), proxy.replayStore, workspace, "tree", "working", ""); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		render("Resumed, three seconds later")
		treeApply(t, proxy, workspace, journalMutation{Op: "set", P: "/1", State: new("done")})
		out.WriteString("Completed\n" + journalNodeRow(u.view.painter.Theme, treeSnapshot(t, proxy, workspace).Items[0].node(), "") + "\n")
		uisnapshot.Assert(t, "testdata/snapshots/journal-active-work-timer.txt", out.String())
	})
}

func TestJournalMainHostStatusPausesWithoutChangingTaskState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proxy, workspace := treeTestJournal(t)
		u := newAppServerSessionTestUI(t, workspace)
		u.thread = "tree"
		u.session.start("tree", workspace)
		u.proxy = proxy
		treeApply(t, proxy, workspace, journalMutation{Op: "add", Kind: "task", Title: new("Work"), State: new("working")})
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "tree", "turn": map[string]any{"id": "one"}})
		time.Sleep(2 * time.Second)
		appServerTestNotify(t, u, "thread/status/changed", map[string]any{"threadId": "tree", "status": map[string]any{"type": "idle"}})
		time.Sleep(time.Hour)
		j := treeSnapshot(t, proxy, workspace)
		if j.Items[0].State != "working" || j.Items[0].WorkTimer.at(time.Now()) != 2*time.Second {
			t.Fatalf("idle task: %+v", j.Items[0])
		}
		appServerTestNotify(t, u, "thread/status/changed", map[string]any{"threadId": "tree", "status": map[string]any{"type": "active"}})
		time.Sleep(3 * time.Second)
		appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "tree", "turn": map[string]any{"id": "one", "status": "completed"}})
		appServerTestNotify(t, u, "thread/status/changed", map[string]any{"threadId": "tree", "status": map[string]any{"type": "idle"}})
		time.Sleep(time.Hour)
		j = treeSnapshot(t, proxy, workspace)
		if j.LifecycleState != "done" || j.Items[0].WorkTimer.at(time.Now()) != 5*time.Second {
			t.Fatalf("completed thread: state=%s timer=%+v", j.LifecycleState, j.Items[0].WorkTimer)
		}
	})
}
