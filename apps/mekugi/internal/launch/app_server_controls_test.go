package launch

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

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
				u.attached.conversation.Queued = []conversationmodel.Input{{Text: "next"}}
			}
			appServerTestKeys(t, u, "/lock\r")
			if !u.shell.Main.InterruptLocked || u.Text != "" {
				t.Fatal("lock not applied locally")
			}
			c := nativeTestCancel(t, u)
			if c.quit || len(c.calls) != 0 {
				t.Fatal("locked Ctrl-C quit or queued host work")
			}
			c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x1b"})
			c.refresh(time.Now())
			if wire.Len() != 0 || u.Interruption.BeforeStart || u.Interruption.Target != "" || !strings.Contains(c.feedback.Text, "/unlock") {
				t.Fatalf("locked cancellation reached host: %s %+v", wire.Bytes(), u)
			}
			if state == "queued" && len(u.attached.conversation.Queued) != 1 {
				t.Fatal("lock discarded queued input")
			}
			if !strings.Contains(ansi.Strip(terminalTest(u).nativeStatus()), "Locked") {
				t.Fatal("lock lacks persistent indicator")
			}
			appServerTestKeys(t, u, "/unlock\r")
			c = nativeTestCancel(t, u)
			quit := c.quit
			if u.shell.Main.InterruptLocked {
				t.Fatal("unlock did not restore keyboard handling")
			}
			if state == "running" && !strings.Contains(wire.String(), "turn/interrupt") || state == "starting" && !u.Interruption.BeforeStart || state == "idle" && !quit || state == "queued" && (len(u.attached.conversation.Queued) != 0 || u.Text != "next") {
				t.Fatalf("unlocked handling did not resume: quit=%v draft=%q wire=%s", quit, u.Text, wire.String())
			}
		})
	}
}

func TestNativeControlsLockedDraftAndContextualEscape(t *testing.T) {
	u, wire := newAppServerTestUI()

	u.Turn, u.shell.Main.InterruptLocked = "active", true
	c := nativeTestPromptClient(u)
	defer c.renderer.terminal.Diff.Close()
	c.renderer.terminal.Main.InterruptLocked = true
	nativeTestInput(c, "draft\x03")
	if c.editor("main").Text != "" || len(c.calls) != 0 {
		t.Fatal("lock broke draft clearing")
	}
	nativeTestInput(c, "\x1a")
	if c.editor("main").Text != "draft" {
		t.Fatal("lock broke draft restoration")
	}
	u.Text = "/"
	u.picker.refreshPicker(&u.attached, u.shell, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
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
	if err := savePaneLayout(p, terminalTest(u).Shell.Preferences(u.shell.Main), time.Now(), true); err != nil {
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
