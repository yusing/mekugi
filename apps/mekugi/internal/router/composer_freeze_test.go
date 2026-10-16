package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func composerShellKeys(t *testing.T, u *appServerUI, text string) {
	t.Helper()
	for _, key := range []byte(text) {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestComposerPastePausedTranscriptThenReference(t *testing.T) {
	image := pasteTestImage(t)
	u, wire := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.Turn = "active"
	u.view.following = false
	composerShellKeys(t, u, "\x1b[200~'"+image+"'\x1b[201~")
	if u.paste || u.shell.input.Pasting() || len(u.Images) != 1 || u.Text != "[Image 1] " || u.view.following {
		t.Fatalf("paste on scrollback: draft=%q image=%v paste=%v/%v following=%v", u.Text, u.Images, u.paste, u.shell.input.Pasting(), u.view.following)
	}
	u.shell.selection = &terminalSelection{Selection: selectedTerminalText("quote"), mention: "message"}
	composerShellKeys(t, u, "rtyped")
	if u.Text != "[Image 1] [Selected message] typed" || len(u.Selections) != 1 || u.Selections[0].Text != "quote" {
		t.Fatalf("reference or typing frozen: %q %+v", u.Text, u.Selections)
	}
	composerShellKeys(t, u, "\x1b[200~'"+image+"'\x1b[201~")
	if len(u.Images) != 2 || strings.Contains(u.Text, "[201~") || strings.Contains(u.Text, image) {
		t.Fatalf("second paste leaked paths or markers: %q", u.Text)
	}
	composerShellKeys(t, u, "\x03")
	if u.Text != "" || wire.Len() != 0 {
		t.Fatalf("Ctrl-C did not clear draft: draft=%q wire=%q", u.Text, wire.String())
	}
	// First Escape returns scrollback to the bottom; the next interrupts.
	for range 2 {
		composerShellKeys(t, u, "\x1b")
		ageTerminalEscape(u.shell)
		if err := u.shell.flushEscape(); err != nil {
			t.Fatal(err)
		}
	}
	appServerOneRequest(t, wire, "turn/interrupt", "")
}

func TestComposerPasteTerminatorBypassesSelection(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	composerShellKeys(t, u, "\x1b[200~payload")
	// A selection must not consume any byte of the opaque end marker.
	u.shell.selection = &terminalSelection{Selection: selectedTerminalText("selected")}
	composerShellKeys(t, u, "\x1b[201~typed")
	if u.Text != "payloadtyped" || u.paste || u.shell.input.Pasting() || wire.Len() != 0 {
		t.Fatalf("selection consumed paste boundary: draft=%q paste=%v/%v", u.Text, u.paste, u.shell.input.Pasting())
	}
}

func TestUISnapshotComposerPasteAndReferenceOnScrollback(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.view.following = false
	composerShellKeys(t, u, "\x1b[200~'"+pasteTestImage(t)+"'\x1b[201~")
	u.shell.selection = &terminalSelection{Selection: selectedTerminalText("quote"), mention: "message"}
	composerShellKeys(t, u, "rtyped")
	rows, _ := u.mainFrame(80, 10, 0)
	uisnapshot.Assert(t, "testdata/snapshots/composer-paste-reference-scrollback.txt", strings.Join(rows, "\n")+"\n")
}
