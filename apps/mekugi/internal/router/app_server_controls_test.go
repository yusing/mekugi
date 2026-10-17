package router

import (
	"bytes"
	"strings"
	"testing"
	"time"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/charmbracelet/x/ansi"
)

func TestNativeControlsLockKeyboardInterrupt(t *testing.T) {
	for _, state := range []string{"running", "starting", "idle", "queued"} {
		t.Run(state, func(t *testing.T) {
			u, wire := newAppServerTestUI()

			switch state {
			case "running":
				u.Turn = "active"
			case "starting":
				u.AwaitingTurn = true
			case "queued":
				u.conversation().Queued = []conversationmodel.Input{{Text: "next"}}
			}
			appServerTestKeys(t, u, "/lock\r")
			if !u.shell.Main.InterruptLocked || u.Text != "" {
				t.Fatal("lock not applied locally")
			}
			quit, err := u.key("\x03")
			if err != nil || quit {
				t.Fatalf("locked Ctrl-C quit=%v err=%v", quit, err)
			}
			if err := terminalTest(u).send("\x1b"); err != nil {
				t.Fatal(err)
			}
			if wire.Len() != 0 || u.Interruption.BeforeStart || u.Interruption.Target != "" || !strings.Contains(u.feedback.Text, "/unlock") {
				t.Fatalf("locked cancellation reached host: %s %+v", wire.Bytes(), u)
			}
			if state == "queued" && len(u.conversation().Queued) != 1 {
				t.Fatal("lock discarded queued input")
			}
			if !strings.Contains(ansi.Strip(terminalTest(u).nativeStatus()), "Locked") {
				t.Fatal("lock lacks persistent indicator")
			}
			appServerTestKeys(t, u, "/unlock\r")
			quit, err = u.key("\x03")
			if err != nil || u.shell.Main.InterruptLocked {
				t.Fatal("unlock did not restore keyboard handling")
			}
			if state == "running" && !strings.Contains(wire.String(), "turn/interrupt") || state == "starting" && !u.Interruption.BeforeStart || state == "idle" && !quit || state == "queued" && (len(u.conversation().Queued) != 0 || u.Text != "next") {
				t.Fatalf("unlocked handling did not resume: quit=%v draft=%q wire=%s", quit, u.Text, wire.String())
			}
		})
	}
}

func TestNativeControlsLockedDraftAndContextualEscape(t *testing.T) {
	u, wire := newAppServerTestUI()

	u.Turn, u.shell.Main.InterruptLocked = "active", true
	appServerTestKeys(t, u, "draft\x03")
	if u.Text != "" {
		t.Fatal("lock broke draft clearing")
	}
	appServerTestKeys(t, u, "\x1a")
	if u.Text != "draft" {
		t.Fatal("lock broke draft restoration")
	}
	u.Text = "/"
	u.refreshPicker()
	u.settlePresentation()
	if err := terminalTest(u).send("\x1b"); err != nil {
		t.Fatal(err)
	}
	if u.picker.Open || wire.Len() != 0 {
		t.Fatal("lock broke contextual picker dismissal")
	}
}

func TestNativeControlsPanePersistence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u := newAppServerSessionTestUI(t, "/workspace")
	u.shell.Main.InterruptLocked = true
	terminalTest(u).DiffOpen = true
	terminalTest(u).Focus = 1
	p := testPanePreferences()
	if err := p.open(&terminalTest(u).Shell, u.shell.Main, "/workspace", "main", false); err != nil {
		t.Fatal(err)
	}
	terminalTest(u).Split = 60
	if err := p.save(terminalTest(u).Shell.Preferences(u.shell.Main), time.Now(), true); err != nil {
		t.Fatal(err)
	}
	resumed := newAppServerSessionTestUI(t, "/workspace")
	if err := p.open(&terminalTest(resumed).Shell, resumed.shell.Main, "/workspace", "main", true); err != nil {
		t.Fatal(err)
	}
	if !resumed.shell.Main.InterruptLocked || !terminalTest(resumed).DiffOpen || terminalTest(resumed).Focus != 1 {
		t.Fatal("resume lost preferences or revived live child state")
	}
	var frame bytes.Buffer
	if err := resumed.testPaint(&frame, 120, 35); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ansi.Strip(frame.String()), "Diff") || !strings.Contains(ansi.Strip(frame.String()), "Locked") {
		t.Fatal("resumed preferences are not rendered")
	}
}
