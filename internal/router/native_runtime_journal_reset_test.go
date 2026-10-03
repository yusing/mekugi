package router

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

type runtimeResetClient struct {
	*runtimeTestClient
	resets []string
}

func (c *runtimeResetClient) Reset(_ context.Context, id string) error {
	c.resets = append(c.resets, id)
	return nil
}

func runtimeSliceFixture(t *testing.T) (*appServerUI, *runtimeResetClient) {
	t.Helper()
	u, s, b, c := nativeRuntimeJournalFixture(t)
	client := &runtimeResetClient{runtimeTestClient: u.runtime.client.(*runtimeTestClient)}
	u.runtime.client = client
	if err := u.beginRuntimeJournalTurn(); err != nil {
		t.Fatal(err)
	}
	input := `{"journal":[{"op":"plan","reset":"slice","tasks":[{"title":"First slice","state":"working"},{"title":"Next slice"}]},{"op":"set","p":"/1","state":"done"}]}`
	runtimeJournalReceipt(t, s, b, c, "slice", "journal_batch", input)
	runtimeJournalInvoke(t, s, c, "slice", "journal_batch", input)
	u.runtime.restoredJournal = observationThread(b)
	if err := u.runtimeEvent(session.Event{Kind: "done", ID: "host-result"}); err != nil {
		t.Fatal(err)
	}
	if u.runtime.continuation == nil || u.runtime.continuation.Resume {
		t.Fatal("slice boundary not selected")
	}
	return u, client
}

func TestNativeRuntimeSliceDispatchAndQueuedInput(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "continue", true: "draft"}[queued], func(t *testing.T) {
			u, c := runtimeSliceFixture(t)
			if err := u.tickRuntimeJournal(u.now().Add(4 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if len(c.resets) != 1 || len(c.sent) != 0 || u.runtime.ready {
				t.Fatal("reset dispatch bypassed initialization")
			}
			if queued {
				u.draft = "user input while initializing"
			}
			if err := u.runtimeEvent(session.Event{Kind: "reset_ready", ID: "other"}); err != nil {
				t.Fatal(err)
			}
			if u.runtime.ready {
				t.Fatal("unmatched reset reply accepted")
			}
			if err := u.runtimeEvent(session.Event{Kind: "reset_ready", ID: c.resets[0]}); err != nil {
				t.Fatal(err)
			}
			if queued {
				if len(c.sent) != 0 || u.draft == "" {
					t.Fatal("pending input lost or automatic turn dispatched")
				}
			} else {
				if len(c.sent) != 1 || !strings.Contains(c.sent[0], "/2 Next slice") || !u.runtime.busy {
					t.Fatalf("missing continuation: %v", c.sent)
				}
			}
			if err := u.runtimeEvent(session.Event{Kind: "reset_ready", ID: c.resets[0]}); err != nil {
				t.Fatal(err)
			}
			if len(c.sent) > 1 {
				t.Fatal("reset receipt replayed a turn")
			}
		})
	}
}

func TestNativeRuntimeContinuationCancellation(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "draft", true: "escape"}[stop], func(t *testing.T) {
			u, c := runtimeSliceFixture(t)
			if stop {
				runtimeKeys(t, u, "\x1b")
				u.shell.sequenceAt = time.Now().Add(-time.Second)
				if err := u.shell.flushEscape(); err != nil {
					t.Fatal(err)
				}
			} else {
				u.draft = "question"
				if err := u.tickRuntimeJournal(u.now().Add(4 * time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if u.runtime.continuation != nil || len(c.resets) != 0 || len(c.sent) != 0 {
				t.Fatal("cancelled plan dispatched")
			}
			o, ctx, b, err := u.runtimeJournalScope()
			if err != nil {
				t.Fatal(err)
			}
			j, _, err := readThreadJournal(o.capture.store.scoped(ctx), b.Workspace, observationThread(b))
			if err != nil {
				t.Fatal(err)
			}
			if (j.StoppedTasks["/2"] != "") != stop || j.ResetIntent != nil {
				t.Fatalf("wrong durable stop: %+v", j.StoppedTasks)
			}
		})
	}
}

func TestNativeRuntimeContinuationWaitsForBackgroundAndSettings(t *testing.T) {
	u, c := runtimeSliceFixture(t)
	u.runtime.tasks = map[string]session.Task{"background": {ID: "background", Status: "running"}}
	u.runtime.settings = &session.Settings{ID: "settings"}
	for range 2 {
		if err := u.tickRuntimeJournal(u.now().Add(4 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.resets) != 0 {
		t.Fatal("reset discarded background work")
	}
	u.runtime.tasks["background"] = session.Task{ID: "background", Status: "completed"}
	if err := u.tickRuntimeJournal(u.now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(c.resets) != 0 {
		t.Fatal("reset raced pending settings")
	}
	u.runtime.settings = nil
	if err := u.tickRuntimeJournal(u.now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(c.resets) != 1 {
		t.Fatal("settled work did not permit reset")
	}
	if err := u.runtimeEvent(session.Event{Kind: "reset_ready", ID: c.resets[0], Failed: true, Text: "native rejected"}); err != nil {
		t.Fatal(err)
	}
	if len(c.sent) != 0 || u.runtime.continuation != nil || !u.runtime.ready {
		t.Fatal("failed reset continued work")
	}
}

func TestNativeRuntimeContinuationRestartDoesNotRepeatDispatch(t *testing.T) {
	u, c := runtimeSliceFixture(t)
	if err := u.tickRuntimeJournal(u.now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(c.resets) != 1 {
		t.Fatal("missing original dispatch")
	}
	next, f := runtimeTestUI(t)
	next.runtime.client = &runtimeResetClient{runtimeTestClient: f}
	next.attachRuntimeObservation(u.runtime.observations)
	if err := next.tickRuntimeJournal(next.now()); err != nil {
		t.Fatal(err)
	}
	if err := next.tickRuntimeJournal(next.now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if next.runtime.continuation != nil || len(f.sent) != 0 {
		t.Fatal("uncertain reset was replayed")
	}
}

func TestUISnapshotNativeRuntimeJournalReset(t *testing.T) {
	u, c := runtimeSliceFixture(t)
	// Exercise the existing shared status renderer without variable journal times.
	u.journal = nil
	u.session.cwd = "/workspace"
	u.runtime.continueAt = u.now().Add(3 * time.Second)
	uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", "native-runtime-reset-countdown.txt"), runtimeFrame(t, u, 100, 20))
	if err := u.tickRuntimeJournal(u.now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", "native-runtime-reset-preparing.txt"), runtimeFrame(t, u, 100, 20))
	if err := u.runtimeEvent(session.Event{Kind: "reset_ready", ID: c.resets[0], Failed: true, Text: "native background task is running"}); err != nil {
		t.Fatal(err)
	}
	uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", "native-runtime-reset-rejected.txt"), runtimeFrame(t, u, 100, 20))
}

func TestNativeRuntimeResetInterruptAndArrowIsolation(t *testing.T) {
	u, c := runtimeSliceFixture(t)
	runtimeKeys(t, u, "\x1b[D")
	if u.runtime.continuation == nil || u.draft != "" {
		t.Fatal("arrow sequence was treated as cancellation or text")
	}
	if err := u.tickRuntimeJournal(u.now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	runtimeKeys(t, u, "\x03")
	if c.interrupts != 1 || u.runtime.continuation != nil {
		t.Fatal("reset interruption did not reach native owner")
	}
	if err := u.runtimeEvent(session.Event{Kind: "reset_ready", ID: c.resets[0]}); err != nil {
		t.Fatal(err)
	}
	if len(c.sent) != 0 {
		t.Fatal("interrupted reset automatically continued")
	}
}

func TestNativeRuntimeManualCompactDoesNotContinuePlan(t *testing.T) {
	u, c := runtimeSliceFixture(t)
	u.runtimeCommands([]session.Command{{Name: "compact", Builtin: true}})
	u.draft = "/compact"
	if handled, _, err := u.runtimeKey('\r'); err != nil || !handled {
		t.Fatalf("manual reset: %v %v", handled, err)
	}
	if len(c.resets) != 1 || u.runtime.continuation != nil || u.draft != "" {
		t.Fatal("manual reset borrowed pending continuation")
	}
	if err := u.runtimeEvent(session.Event{Kind: "reset_ready", ID: c.resets[0]}); err != nil {
		t.Fatal(err)
	}
	if len(c.sent) != 0 {
		t.Fatal("manual reset dispatched a model turn")
	}
}

func TestNativeRuntimeInterruptBeforeNativeIdentity(t *testing.T) {
	for _, afterReset := range []bool{false, true} {
		t.Run(map[bool]string{false: "first-input", true: "reset-input"}[afterReset], func(t *testing.T) {
			u, client := runtimeTestUI(t)
			service, binding, httpClient := observationHTTPFixture(t)
			service.EnableJournal()
			u.attachRuntimeObservation(service)
			if afterReset {
				runtimeJournalBind(t, service, binding, httpClient)
				runtimeJournalAdd(t, service, binding, httpClient, "task", "Existing work")
				binding.Session = "fresh-reset"
				if _, err := service.journal.resetSession(t.Context(), "journal_reset", binding); err != nil {
					t.Fatal(err)
				}
			}
			u.draft = "first input before native identity"
			if _, _, err := u.runtimeKey('\r'); err != nil {
				t.Fatal(err)
			}
			if !u.runtime.busy || len(client.sent) != 1 {
				t.Fatal("input was not sent")
			}
			if _, _, err := u.runtimeKey(3); err != nil || client.interrupts != 1 {
				t.Fatalf("native interruption blocked: %v count=%d", err, client.interrupts)
			}
			runtimeJournalBind(t, service, binding, httpClient)
			j := runtimeResetJournal(t, service, binding)
			if j.ResetHandledTurn != u.runtime.turn || service.journal.pendingStop != "" {
				t.Fatal("pending stop was not retained on native binding")
			}
			if afterReset && j.StoppedTasks["/1"] != u.runtime.turn {
				t.Fatal("reset work was not paused")
			}
		})
	}
}

func TestNativeRuntimeInterruptSurvivesJournalFailure(t *testing.T) {
	for _, key := range []string{"\x03", "\x1b"} {
		t.Run(fmt.Sprintf("key-%x", key), func(t *testing.T) {
			u, service, _, _ := nativeRuntimeJournalFixture(t)
			client := u.runtime.client.(*runtimeTestClient)
			u.draft = "native work"
			runtimeKeys(t, u, "\r")
			service.owner.store.directory = filepath.Join(t.TempDir(), "missing-store")
			runtimeKeys(t, u, key)
			u.shell.sequenceAt = time.Now().Add(-time.Second)
			if err := u.shell.flushEscape(); err != nil || client.interrupts != 1 {
				t.Fatalf("journal failure blocked native interrupt: err=%v count=%d", err, client.interrupts)
			}
		})
	}
}

func TestNativeRuntimeKeyboardInterruptRetainsJournalStop(t *testing.T) {
	for _, key := range []string{"\x03", "\x1b"} {
		t.Run(fmt.Sprintf("key-%x", key), func(t *testing.T) {
			u, service, binding, httpClient := nativeRuntimeJournalFixture(t)
			runtimeJournalAdd(t, service, binding, httpClient, "task", "Existing work")
			u.draft = "continue working"
			runtimeKeys(t, u, "\r")
			runtimeKeys(t, u, key)
			u.shell.sequenceAt = time.Now().Add(-time.Second)
			if err := u.shell.flushEscape(); err != nil {
				t.Fatal(err)
			}
			if u.runtime.client.(*runtimeTestClient).interrupts != 1 {
				t.Fatal("native interrupt was not sent exactly once")
			}
			if err := u.runtimeEvent(session.Event{Kind: "done", ID: "racing-success"}); err != nil {
				t.Fatal(err)
			}
			if u.runtime.continuation != nil {
				t.Fatal("racing success continued stopped journal work")
			}
			j := runtimeResetJournal(t, service, binding)
			if j.StoppedTasks["/1"] != u.runtime.turn {
				t.Fatal("keyboard interrupt did not retain the journal stop")
			}
		})
	}
}

func TestNativeRuntimeResetInterruptSurvivesJournalFailure(t *testing.T) {
	u, client := runtimeSliceFixture(t)
	if err := u.tickRuntimeJournal(u.now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	u.runtime.observations.owner.store.directory = filepath.Join(t.TempDir(), "missing-store")
	if _, quit, err := u.runtimeKey(3); err != nil || quit || client.interrupts != 1 || u.runtime.continuation != nil {
		t.Fatalf("journal failure blocked reset interrupt: quit=%v err=%v count=%d", quit, err, client.interrupts)
	}
}
