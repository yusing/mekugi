package router

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func nativeRuntimeJournalFixture(t *testing.T) (*appServerUI, *ObservationService, ObservationBinding, *http.Client) {
	t.Helper()
	u, _ := runtimeTestUI(t)
	s, b, c := runtimeJournalFixture(t)
	u.attachRuntimeObservation(s)
	u.journal = s.journal.sink()
	return u, s, b, c
}
func TestNativeRuntimeJournalAcknowledgesOnlyPaintedRevision(t *testing.T) {
	u, s, b, c := nativeRuntimeJournalFixture(t)
	runtimeJournalAdd(t, s, b, c, "first", "Working")
	pending := u.runtimeJournalPending()
	if len(pending) != 1 || !u.dirty {
		t.Fatal("pending revision not applied")
	}
	u.mainContentPainted = false
	if err := u.acknowledgeRuntimeJournal(pending); err != nil {
		t.Fatal(err)
	}
	if len(u.journal.snapshot()) != 1 {
		t.Fatal("unpainted publication acknowledged")
	}
	runtimeJournalAdd(t, s, b, c, "second", "Newer")
	runtimeFrame(t, u, 120, 32)
	if err := u.acknowledgeRuntimeJournal(pending); err != nil {
		t.Fatal(err)
	}
	newer := u.journal.snapshot()
	if len(newer) != 1 || newer[0].event.Fields.Title != "Newer" {
		t.Fatalf("paint receipt consumed newer event: %+v", newer)
	}
	pending = u.runtimeJournalPending()
	runtimeFrame(t, u, 120, 32)
	if err := u.acknowledgeRuntimeJournal(pending); err != nil {
		t.Fatal(err)
	}
	if len(u.journal.snapshot()) != 0 {
		t.Fatal("displayed revisions remain pending")
	}
}
func TestNativeRuntimeJournalDonePreservesTaskAndAnswer(t *testing.T) {
	u, s, b, c := nativeRuntimeJournalFixture(t)
	runtimeJournalAdd(t, s, b, c, "task", "Still working")
	const answer = "The substantive native answer stays unchanged."
	if err := u.runtimeEvent(session.Event{Kind: "message", ID: "answer", Text: answer}); err != nil {
		t.Fatal(err)
	}
	if err := u.runtimeEvent(session.Event{Kind: "done", ID: "turn"}); err != nil {
		t.Fatal(err)
	}
	pending := u.runtimeJournalPending()
	cards := 0
	for _, p := range pending {
		if p.card != nil {
			cards++
			if p.card.Journal.Items[0].State != "working" {
				t.Fatal("done mutated task")
			}
		}
	}
	if cards != 1 {
		t.Fatalf("missing terminal report: %+v", pending)
	}
	nodes := runtimeJournalRead(t, s, b, c, "read", `{}`)
	if len(nodes) != 1 || nodes[0].State != "working" {
		t.Fatalf("done persisted task completion: %+v", nodes)
	}
	if u.journal.hides("answer") {
		t.Fatal("journal hid native answer")
	}
	frame := runtimeFrame(t, u, 120, 32)
	if !strings.Contains(frame, answer) {
		t.Fatalf("native answer lost:\n%s", frame)
	}
	if err := u.acknowledgeRuntimeJournal(pending); err != nil {
		t.Fatal(err)
	}
	if len(u.journal.snapshot()) != 0 {
		t.Fatal("painted report not acknowledged")
	}
}
func TestUISnapshotNativeRuntimeJournalMainAndPlan(t *testing.T) {
	for _, width := range []int{42, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, s, b, c := nativeRuntimeJournalFixture(t)
			u.view.clock = u.clock
			runtimeJournalAdd(t, s, b, c, "task", "Validate native journal")
			input := `{"journal":[{"op":"log","p":"/1","text":"Receipts persist before publication."}]}`
			runtimeJournalReceipt(t, s, b, c, "note", "journal_batch", input)
			runtimeJournalInvoke(t, s, c, "note", "journal_batch", input)
			// Freeze both the pane's mounted projection and pending transcript events.
			stamp := u.now().Format(time.RFC3339Nano)
			freezeNode := func(n *journalNode) {
				n.Created.At, n.Updated.At = stamp, stamp
				if n.Started != nil {
					n.Started.At = stamp
				}
				if n.Finished != nil {
					n.Finished.At = stamp
				}
			}
			u.journal.mu.Lock()
			for _, j := range []*threadJournal{u.journal.tree, u.journal.mounted} {
				if j == nil {
					continue
				}
				for i := range j.Events {
					j.Events[i].At = stamp
					freezeNode(&j.Events[i].Fields)
				}
				for i := range j.Items {
					j.Items[i].CreatedAt, j.Items[i].UpdatedAt = stamp, stamp
					if j.Items[i].Started != nil {
						j.Items[i].Started.At = stamp
					}
					if j.Items[i].Finished != nil {
						j.Items[i].Finished.At = stamp
					}
				}
			}
			for _, p := range u.journal.pending {
				if p.event != nil {
					p.event.At = stamp
					freezeNode(&p.event.Fields)
				}
			}
			u.journal.mu.Unlock()
			u.runtimeJournalPending()
			if err := u.runtimeEvent(session.Event{Kind: "message", ID: "answer", Text: "Native tools retain execution authority."}); err != nil {
				t.Fatal(err)
			}
			u.shell.journalOpen = true
			u.shell.focus = 4
			u.session.cwd = "/workspace"
			frame := strings.ReplaceAll(runtimeFrame(t, u, width, 32), b.Workspace, "/workspace")
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-journal-%d.txt", width)), frame)
			if width == 42 {
				u.shell.journalOpen = false
				u.shell.focus = 0
				frame = strings.ReplaceAll(runtimeFrame(t, u, width, 32), b.Workspace, "/workspace")
				uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", "native-runtime-journal-main-42.txt"), frame)
			}
		})
	}
}

func TestNativeRuntimeJournalPanePaintDoesNotConsumeMainReport(t *testing.T) {
	u, s, b, c := nativeRuntimeJournalFixture(t)
	runtimeJournalAdd(t, s, b, c, "task", "Still working")
	if err := u.runtimeEvent(session.Event{Kind: "done", ID: "turn"}); err != nil {
		t.Fatal(err)
	}
	pending := u.runtimeJournalPending()
	u.shell.journalOpen = true
	u.shell.focus = 4
	runtimeFrame(t, u, 42, 32)
	if u.mainContentPainted {
		t.Fatal("narrow Journal unexpectedly presented Main")
	}
	if err := u.acknowledgeRuntimeJournal(pending); err != nil {
		t.Fatal(err)
	}
	remaining := u.journal.snapshot()
	if len(remaining) != 1 || remaining[0].card == nil {
		t.Fatalf("pane paint consumed unshown report or retained shown event: %+v", remaining)
	}
	u.shell.journalOpen = false
	u.shell.focus = 0
	pending = u.runtimeJournalPending()
	runtimeFrame(t, u, 42, 32)
	if err := u.acknowledgeRuntimeJournal(pending); err != nil {
		t.Fatal(err)
	}
	if len(u.journal.snapshot()) != 0 {
		t.Fatal("Main paint did not acknowledge report")
	}
}
