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
			if wire.Len() != 0 || u.interruption.beforeStart || u.interruption.target != "" || !strings.Contains(u.feedback.Text, "/unlock") {
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

func TestNativeControlsPanePersistence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u := newAppServerSessionTestUI(t, "/workspace")
	u.interruptLocked = true
	u.shell.diffOpen = true
	u.shell.focus = 1
	p := testPanePreferences()
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
	if !resumed.interruptLocked || !resumed.shell.diffOpen || resumed.shell.focus != 1 {
		t.Fatal("resume lost preferences or revived live child state")
	}
	var frame bytes.Buffer
	if err := resumed.paint(&frame, 120, 35); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ansi.Strip(frame.String()), "Diff") || !strings.Contains(ansi.Strip(frame.String()), "Locked") {
		t.Fatal("resumed preferences are not rendered")
	}
}
