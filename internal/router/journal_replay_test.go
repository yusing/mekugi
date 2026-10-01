package router

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func journalReplayFixture(t *testing.T) (*mekugiProxy, string, threadJournal, []appServerHistoryTurn, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, proxy, _, workspace := newMekugiTestTransform(t)
	directory, err := defaultMekugiReplayDirectory()
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore, err = openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "root", "/root", ""); err != nil {
		t.Fatal(err)
	}
	for i, mutations := range [][]journalMutation{
		{{Op: "add", Kind: "task", Title: new("Restore journal history"), State: new("working")}},
		{{Op: "log", P: "/1", Text: new("First check passed")}},
		{{Op: "set", P: "/1", State: new("done")}},
		{{Op: "set", P: "/1", State: new("working")}},
		{{Op: "set", P: "/1", Title: new("Reopened task")}},
		{{Op: "log", P: "/1", Text: new("Second check passed")}},
		{{Op: "set", P: "/1", State: new("done")}},
	} {
		if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "root", string(rune('a'+i)), mutations); err != nil {
			t.Fatal(err)
		}
	}
	j, _, err := readThreadJournal(proxy.replayStore, workspace, "root")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 30, 8, 0, 0, 0, time.Local)
	for i := range j.Events {
		stamp := start.Add(time.Duration(i+1) * time.Second).Format(time.RFC3339Nano)
		j.Events[i].At = stamp
		j.Events[i].Fields.Created.At, j.Events[i].Fields.Updated.At = stamp, stamp
		j.Events[i].Fields.Started, j.Events[i].Fields.Finished = nil, nil
	}
	for i := range j.Items {
		for _, event := range j.Events {
			if event.Path == j.Items[i].Path {
				j.Items[i].CreatedAt, j.Items[i].UpdatedAt = event.Fields.Created.At, event.Fields.Updated.At
				j.Items[i].Started, j.Items[i].Finished = nil, nil
			}
		}
	}
	j.LiveSeq, j.FlushSeq = j.Sequence, j.Sequence
	if err := writeThreadJournal(proxy.replayStore, j); err != nil {
		t.Fatal(err)
	}
	turns := []appServerHistoryTurn{
		{ID: "first", Status: "completed", StartedAt: start.Unix(), CompletedAt: start.Add(3 * time.Second).Unix()},
		{ID: "second", Status: "completed", StartedAt: start.Add(4 * time.Second).Unix(), CompletedAt: start.Add(8 * time.Second).Unix()},
	}
	meta := replayTestMeta("root")
	meta["payload"].(map[string]any)["cwd"] = workspace
	epoch := start.UnixMilli()
	path := replayTestWrite(t, t.TempDir(), "root.jsonl", meta,
		replayTestRecord("event_msg", epoch, map[string]any{"type": "task_started", "turn_id": "first"}),
		replayTestRecord("event_msg", epoch+3999, map[string]any{"type": "task_complete", "turn_id": "first"}),
		replayTestRecord("event_msg", epoch+4000, map[string]any{"type": "task_started", "turn_id": "second"}),
		replayTestRecord("event_msg", epoch+8999, map[string]any{"type": "task_complete", "turn_id": "second"}))
	return proxy, workspace, j, turns, path
}

func TestJournalResumeRestoresAcknowledgedEventsAndCompletionCards(t *testing.T) {
	proxy, workspace, j, turns, _ := journalReplayFixture(t)
	u := newAppServerSessionTestUI(t, workspace)
	u.thread, u.proxy = "root", proxy
	before, err := os.ReadFile(filepath.Join(proxy.replayStore.directory, journalFilename(workspace, "root")))
	if err != nil {
		t.Fatal(err)
	}
	u.restoreHistory(turns)
	var cards []*nativeJournalCard
	for _, entry := range u.view.entries {
		if entry.journalCard != nil {
			cards = append(cards, entry.journalCard)
		}
	}
	if len(u.view.entries) != 8 || len(cards) != 2 {
		t.Fatalf("history lost events or cards: entries=%d cards=%d", len(u.view.entries), len(cards))
	}
	first, second := journalMainCard(cards[0].Journal, cards[0].Since, true), journalMainCard(cards[1].Journal, cards[1].Since, true)
	if !strings.Contains(first, "Restore journal history") || strings.Contains(first, "Second check") || !strings.Contains(second, "Reopened task") || strings.Contains(second, "First check") {
		t.Fatalf("cards mixed turn snapshots:\n%s\n%s", first, second)
	}
	after, err := os.ReadFile(filepath.Join(proxy.replayStore.directory, journalFilename(workspace, "root")))
	if err != nil || !bytes.Equal(before, after) || j.FlushSeq != j.Sequence {
		t.Fatal("restoring history changed durable journal state")
	}
}

func TestSessionUIReplayRestoresJournalAndRewindsPaneState(t *testing.T) {
	proxy, workspace, _, _, path := journalReplayFixture(t)
	if err := os.Remove(filepath.Join(proxy.replayStore.directory, "store.lock")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(proxy.replayStore.directory, journalFilename(workspace, "root")))
	if err != nil {
		t.Fatal(err)
	}
	source, err := readSessionUIReplay(t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	p := newUIReplayPlayback(t.Context(), source, 1)
	t.Cleanup(p.close)
	for _, at := range []time.Duration{p.until, 2 * time.Second, p.until} {
		if err := p.seek(at); err != nil {
			t.Fatal(err)
		}
		j := p.ui.journalTreeSnapshot()
		if j == nil || len(j.Items) == 0 {
			t.Fatal("Journal pane lost restored state")
		}
		if at == 2*time.Second {
			if j.Items[0].State != "working" || len(j.Items) != 2 {
				t.Fatal("backward seek retained future journal revisions")
			}
		} else if j.Items[0].State != "done" || j.Items[0].Title != "Reopened task" {
			t.Fatal("completed replay did not restore current journal")
		}
	}
	after, err := os.ReadFile(filepath.Join(proxy.replayStore.directory, journalFilename(workspace, "root")))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("offline replay modified journal")
	}
	if _, err := os.Lstat(filepath.Join(proxy.replayStore.directory, "store.lock")); !os.IsNotExist(err) {
		t.Fatalf("offline replay acquired storage resources: %v", err)
	}
}

func TestJournalReplayDoesNotInventCompletion(t *testing.T) {
	_, _, j, turns, _ := journalReplayFixture(t)
	for _, status := range []string{"failed", "interrupted", "inProgress", "completed"} {
		for _, flushed := range []bool{false, true} {
			copy := j.clone()
			if !flushed {
				copy.FlushSeq = 0
			}
			windows := []journalReplayTurn{{id: "first", status: status, start: historyTime(turns[0].StartedAt), end: historyTime(turns[0].CompletedAt)}}
			cards := 0
			for _, update := range journalReplayTimeline(copy, windows) {
				if update.publication.card != nil {
					cards++
				}
			}
			if (cards == 1) != (status == "completed" && flushed) {
				t.Fatalf("status=%s flushed=%v cards=%d", status, flushed, cards)
			}
		}
	}
}

func TestUISnapshotSessionUIReplayRestoredJournal(t *testing.T) {
	_, _, _, _, path := journalReplayFixture(t)
	source, err := readSessionUIReplay(t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	p := newUIReplayPlayback(t.Context(), source, 1)
	t.Cleanup(p.close)
	if err := p.seek(p.until); err != nil {
		t.Fatal(err)
	}
	p.paused = true
	p.ui.view.painter.Theme, p.ui.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
	uisnapshot.Assert(t, "testdata/snapshots/session-ui-replay-restored-completion.txt", replayPlaybackTestPaint(t, p, 120, 28))
	p.key(2)
	p.key('5')
	uisnapshot.Assert(t, "testdata/snapshots/session-ui-replay-restored-pane.txt", replayPlaybackTestPaint(t, p, 120, 28))
}

func TestJournalReplayInlineReportsAreNotDuplicated(t *testing.T) {
	proxy, workspace, _, turns, _ := journalReplayFixture(t)
	const id = "retained-inline-report"
	if err := proxy.replayStore.putCommentary(t.Context(), workspace, []string{id}); err != nil {
		t.Fatal(err)
	}
	turns[0].Items = []appServerItem{{ID: id, Type: "agentMessage", Text: "Journal\n- First check passed"}}
	u := newAppServerSessionTestUI(t, workspace)
	u.thread, u.proxy = "root", proxy
	u.restoreHistory(turns)
	if len(u.view.entries) != 5 || u.view.entries[0].Text != turns[0].Items[0].Text {
		t.Fatalf("inline report duplicated or hidden: %+v", u.view.entries)
	}
	// Text resemblance alone must not suppress journal evidence.
	turns[0].Items[0].ID = "ordinary-provider-message"
	u = newAppServerSessionTestUI(t, workspace)
	u.thread, u.proxy = "root", proxy
	u.restoreHistory(turns)
	if len(u.view.entries) != 9 {
		t.Fatal("unverified Journal-looking answer suppressed durable evidence")
	}
}

func TestJournalReplayInheritedFactsArePaneStateNotLocalReports(t *testing.T) {
	_, _, j, turns, _ := journalReplayFixture(t)
	windows := []journalReplayTurn{{id: "fork-local", status: "completed", start: historyTime(turns[1].CompletedAt), end: historyTime(turns[1].CompletedAt).Add(time.Second)}}
	updates := journalReplayTimeline(j, windows)
	if len(updates) != 1 || updates[0].tree == nil || len(updates[0].tree.Items) != 3 || updates[0].tree.Items[0].State != "done" {
		t.Fatal("inherited facts lost, or revived as a new completion")
	}
}

func TestUISnapshotJournalResumedCompletion(t *testing.T) {
	proxy, workspace, _, turns, _ := journalReplayFixture(t)
	u := newAppServerSessionTestUI(t, workspace)
	u.thread, u.proxy = "root", proxy
	proxy.journals = newJournalStore()
	u.journal = proxy.journals.attachNative(workspace, "root")
	t.Cleanup(func() { proxy.journals.detachNative(u.journal) })
	if err := proxy.journals.restoreNative(t.Context(), proxy.replayStore, u.journal); err != nil {
		t.Fatal(err)
	}
	u.clock = func() time.Time { return historyTime(turns[1].CompletedAt).Add(time.Second) }
	u.view.painter.Theme, u.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
	u.restoreHistory(turns)
	screen := vt.NewEmulator(120, 28)
	defer screen.Close()
	if err := u.paint(screen, 120, 28); err != nil {
		t.Fatal(err)
	}
	uisnapshot.Assert(t, "testdata/snapshots/journal-resumed-completion.txt", screen.String())
}

func TestJournalResumePartitionsAdjacentSubsecondTurns(t *testing.T) {
	proxy, workspace, j, turns, _ := journalReplayFixture(t)
	start := historyTime(turns[0].StartedAt)
	for i := range j.Events {
		offset := time.Duration(i+1) * 10 * time.Millisecond
		if i >= 3 {
			offset += 400 * time.Millisecond
		}
		j.Events[i].At = start.Add(offset).Format(time.RFC3339Nano)
	}
	if err := writeThreadJournal(proxy.replayStore, j); err != nil {
		t.Fatal(err)
	}
	for i := range turns {
		turns[i].StartedAt, turns[i].CompletedAt = start.Unix(), start.Unix()
	}
	epoch := start.UnixMilli()
	path := replayTestWrite(t, t.TempDir(), "root.jsonl", replayTestMeta("root"),
		replayTestRecord("event_msg", epoch, map[string]any{"type": "task_started", "turn_id": "first"}),
		replayTestRecord("event_msg", epoch+100, map[string]any{"type": "task_complete", "turn_id": "first"}),
		replayTestRecord("event_msg", epoch+400, map[string]any{"type": "task_started", "turn_id": "second"}),
		replayTestRecord("event_msg", epoch+700, map[string]any{"type": "task_complete", "turn_id": "second"}))
	u := newAppServerSessionTestUI(t, workspace)
	u.thread, u.proxy = "root", proxy
	u.session.start("root", workspace)
	u.restoring = &appServerActivityRestore{root: appServerThreadInfo{ID: "root", Cwd: workspace, Path: path}}
	u.readRestoredRollouts()
	u.restoreHistory(turns)
	var reports []string
	for _, entry := range u.view.entries {
		if entry.journalCard != nil {
			reports = append(reports, journalMainCard(entry.journalCard.Journal, entry.journalCard.Since, true))
		}
	}
	if len(reports) != 2 || !strings.Contains(reports[0], "First check") || strings.Contains(reports[0], "Second check") || !strings.Contains(reports[1], "Second check") {
		t.Fatalf("same-second turns mixed cards: %v", reports)
	}
	// Without precise evidence the overlapping windows cannot prove cards.
	u = newAppServerSessionTestUI(t, workspace)
	u.thread, u.proxy = "root", proxy
	u.restoreHistory(turns)
	for _, entry := range u.view.entries {
		if entry.journalCard != nil {
			t.Fatal("ambiguous whole-second timing invented a completion window")
		}
	}
}

func TestSessionUIReplayRetainsResetNotesBetweenAndAfterTurns(t *testing.T) {
	for _, at := range []time.Duration{3500 * time.Millisecond, 9 * time.Second} {
		proxy, _, j, turns, path := journalReplayFixture(t)
		if _, err := j.applyTree(journalMutation{Op: "log", P: "/1", Text: new("↻ Context reset from journal · 0 provider tokens")}); err != nil {
			t.Fatal(err)
		}
		reset := &j.Events[len(j.Events)-1]
		reset.Op, reset.At = "reset", historyTime(turns[0].StartedAt).Add(at).Format(time.RFC3339Nano)
		if at < 4*time.Second {
			// Place the real node-bearing reset revision between the two turns,
			// preserving the journal's sequence/time order.
			event := *reset
			event.Seq = 4
			for i := 3; i < len(j.Events)-1; i++ {
				j.Events[i].Seq++
			}
			j.Events = append(append(append([]journalEvent{}, j.Events[:3]...), event), j.Events[3:len(j.Events)-1]...)
		}
		if err := writeThreadJournal(proxy.replayStore, j); err != nil {
			t.Fatal(err)
		}
		source, err := readSessionUIReplay(t.Context(), path, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		p := newUIReplayPlayback(t.Context(), source, 1)
		if err := p.seek(p.until); err != nil {
			p.close()
			t.Fatal(err)
		}
		found := false
		for _, item := range p.ui.journalTreeSnapshot().Items {
			found = found || strings.Contains(item.Title, "Context reset")
		}
		rows := 0
		for _, entry := range p.ui.view.entries {
			if entry.journalEvent != nil && entry.journalEvent.Op == "reset" {
				rows++
			}
		}
		p.close()
		if !found || rows != 1 {
			t.Fatalf("reset at %v lost: node=%v rows=%d", at, found, rows)
		}
	}
}

func TestSessionUIReplayUnavailableJournalPreservesHostActivity(t *testing.T) {
	proxy, workspace, _, _, path := journalReplayFixture(t)
	if err := os.WriteFile(filepath.Join(proxy.replayStore.directory, journalFilename(workspace, "root")), []byte("invalid journal"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := readSessionUIReplay(t.Context(), path, "", 1)
	if err != nil || len(source.JournalUnavailable) != 1 || len(source.Events) != 4 {
		t.Fatalf("journal failure blocked or discarded host replay: source=%+v err=%v", source, err)
	}
}
