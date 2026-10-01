package router

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestNativeControlsLockKeyboardInterrupt(t *testing.T) {
	for _, state := range []string{"running", "starting", "idle", "queued"} {
		t.Run(state, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.ensureShell()
			switch state {
			case "running":
				u.turn = "active"
			case "starting":
				u.awaitingTurn = true
			case "queued":
				u.queued = []composerDraft{{text: "next"}}
			}
			appServerTestKeys(t, u, "/lock\r")
			if !u.interruptLocked || u.draft != "" {
				t.Fatal("lock not applied locally")
			}
			quit, err := u.key(3)
			if err != nil || quit {
				t.Fatalf("locked Ctrl-C quit=%v err=%v", quit, err)
			}
			if err := u.shell.send("\x1b"); err != nil {
				t.Fatal(err)
			}
			if wire.Len() != 0 || u.interruption.beforeStart || u.interruption.target != "" || !strings.Contains(u.notice, "/unlock") {
				t.Fatalf("locked cancellation reached host: %s %+v", wire.Bytes(), u)
			}
			if state == "queued" && len(u.queued) != 1 {
				t.Fatal("lock discarded queued input")
			}
			if !strings.Contains(ansi.Strip(u.shell.nativeStatus()), "Locked") {
				t.Fatal("lock lacks persistent indicator")
			}
			appServerTestKeys(t, u, "/unlock\r")
			quit, err = u.key(3)
			if err != nil || u.interruptLocked {
				t.Fatal("unlock did not restore keyboard handling")
			}
			if state == "running" && !strings.Contains(wire.String(), "turn/interrupt") || state == "starting" && !u.interruption.beforeStart || state == "idle" && !quit || state == "queued" && (len(u.queued) != 0 || u.draft != "next") {
				t.Fatalf("unlocked handling did not resume: quit=%v draft=%q wire=%s", quit, u.draft, wire.String())
			}
		})
	}
}

func TestNativeControlsLockedDraftAndContextualEscape(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ensureShell()
	u.turn, u.interruptLocked = "active", true
	appServerTestKeys(t, u, "draft\x03")
	if u.draft != "" {
		t.Fatal("lock broke draft clearing")
	}
	appServerTestKeys(t, u, "\x1a")
	if u.draft != "draft" {
		t.Fatal("lock broke draft restoration")
	}
	u.draft = "/"
	u.refreshPicker()
	if err := u.shell.send("\x1b"); err != nil {
		t.Fatal(err)
	}
	if u.picker.open || wire.Len() != 0 {
		t.Fatal("lock broke contextual picker dismissal")
	}
}

func TestNativeControlsJournalActivityLifecycle(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	if !u.shell.journalOpen {
		t.Fatal("new session does not default to Journal")
	}
	spawn := func(thread, path string) {
		appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": thread, "agentNickname": path}})
	}
	finish := func(thread string) {
		appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t", "status": "completed"}})
	}
	spawn("a", "a")
	if u.shell.journalOpen || !u.shell.autoActivity || u.shell.focus != 0 || !u.shell.paneState().JournalOpen {
		t.Fatal("spawn did not temporarily show Activity while retaining Journal preference")
	}
	spawn("b", "b")
	finish("a")
	if u.shell.journalOpen {
		t.Fatal("one completion hid outstanding child")
	}
	finish("b")
	if !u.shell.journalOpen || u.shell.autoActivity {
		t.Fatal("last completion did not return to Journal")
	}
	// A later follow-up reopens Activity without another spawn.
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "a", "turn": map[string]any{"id": "again"}})
	if u.shell.journalOpen {
		t.Fatal("follow-up did not show Activity")
	}
	for _, key := range []byte("\x022") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	finish("a")
	if !u.shell.diffOpen || u.shell.journalOpen {
		t.Fatal("completion overwrote user's Diff selection")
	}
	spawn("c", "c")
	if !u.shell.diffOpen || u.shell.autoActivity {
		t.Fatal("spawn overwrote user's Diff selection")
	}
}

func TestNativeControlsPanePersistence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u := newAppServerSessionTestUI(t, "/workspace")
	u.interruptLocked = true
	u.shell.journalOpen, u.shell.autoActivity = false, true
	p := new(nativePanePersistence)
	if err := p.open(u.shell, "/workspace", "main", false); err != nil {
		t.Fatal(err)
	}
	u.shell.split = 60
	if err := p.save(u.shell, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	resumed := newAppServerSessionTestUI(t, "/workspace")
	if err := p.open(resumed.shell, "/workspace", "main", true); err != nil {
		t.Fatal(err)
	}
	if !resumed.interruptLocked || !resumed.shell.journalOpen || resumed.shell.autoActivity || len(resumed.activeChildren) != 0 {
		t.Fatal("resume lost preferences or revived live child state")
	}
	var frame bytes.Buffer
	if err := resumed.paint(&frame, 120, 35); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ansi.Strip(frame.String()), "Journal") || !strings.Contains(ansi.Strip(frame.String()), "Locked") {
		t.Fatal("resumed preferences are not rendered")
	}
}

func TestNativeControlsManualJournalSurvivesOverlappingChildren(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	spawn := func(thread string) {
		appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": thread}})
	}
	finish := func(thread string) {
		appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "t", "status": "completed"}})
	}
	spawn("a")
	for _, key := range []byte("\x025") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	spawn("b")
	if !u.shell.journalOpen || u.shell.autoActivity || u.shell.focus != 4 {
		t.Fatal("overlapping child replaced manual Journal selection or focus")
	}
	finish("a")
	finish("b")
	if !u.shell.journalOpen || u.shell.focus != 4 {
		t.Fatal("completion replaced manual Journal selection")
	}
	spawn("c")
	if u.shell.journalOpen || !u.shell.autoActivity {
		t.Fatal("a new child lifecycle did not reveal Activity")
	}
}

func TestNativeControlsActivityEventsAndManualSelection(t *testing.T) {
	for _, manual := range []bool{false, true} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		notify := func(kind string) {
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
				"id": "agent-" + kind, "type": "subAgentActivity", "agentThreadId": "child", "agentPath": "/root/worker", "kind": kind}})
		}
		u.shell.focus = 4
		notify("started")
		if !u.shell.autoActivity || u.shell.journalOpen || u.shell.focus != 0 {
			t.Fatal("authentic started item did not show Activity and release hidden Journal focus")
		}
		if manual {
			for _, key := range []byte{2, '3'} {
				if err := u.shell.key(key); err != nil {
					t.Fatal(err)
				}
			}
		}
		notify("completed")
		if u.shell.journalOpen == manual || u.shell.autoActivity || len(u.activeChildren) != 0 {
			t.Fatal("completed item did not respect explicit Activity preference")
		}
	}
}

func TestNativeControlsClearRestoresPreferredJournal(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.shell.journalOpen, u.shell.autoActivity = false, true
	u.activeChildren = map[string]bool{"child": true}
	if err := u.clearSessionPresentation(); err != nil {
		t.Fatal(err)
	}
	if !u.shell.journalOpen || u.shell.autoActivity || len(u.activeChildren) != 0 {
		t.Fatal("clearing a session persisted transient child Activity")
	}
}
