package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func composerShellKeys(t *testing.T, u *appServerUI, text string) {
	t.Helper()
	for _, key := range []byte(text) {
		if err := terminalTest(u).key(key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestComposerPastePausedTranscriptThenReference(t *testing.T) {
	image := pasteTestImage(t)
	u, wire := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { terminalTest(u).Diff.Close() })
	u.Turn = "active"
	u.view.Following = false
	composerShellKeys(t, u, "\x1b[200~'"+image+"'\x1b[201~")
	if terminalTest(u).Decoder.Pasting() || len(u.Images) != 1 || u.Text != "[Image 1] " || u.view.Following {
		t.Fatalf("paste on scrollback: draft=%q image=%v paste=%v following=%v", u.Text, u.Images, terminalTest(u).Decoder.Pasting(), u.view.Following)
	}
	terminalTest(u).Selection = &terminalsession.SourceSelection{Selection: selectedTerminalText("quote"), Mention: "message"}
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
		ageTerminalEscape(terminalTest(u))
		if err := terminalTest(u).flushEscape(); err != nil {
			t.Fatal(err)
		}
	}
	appServerOneRequest(t, wire, "turn/interrupt", "")
}

func TestComposerPasteTerminatorBypassesSelection(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { terminalTest(u).Diff.Close() })
	composerShellKeys(t, u, "\x1b[200~payload")
	// A selection must not consume any byte of the opaque end marker.
	terminalTest(u).Selection = &terminalsession.SourceSelection{Selection: selectedTerminalText("selected")}
	composerShellKeys(t, u, "\x1b[201~typed")
	if u.Text != "payloadtyped" || terminalTest(u).Decoder.Pasting() || wire.Len() != 0 {
		t.Fatalf("selection consumed paste boundary: draft=%q paste=%v", u.Text, terminalTest(u).Decoder.Pasting())
	}
}

func TestUISnapshotComposerPasteAndReferenceOnScrollback(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { terminalTest(u).Diff.Close() })
	u.view.Following = false
	composerShellKeys(t, u, "\x1b[200~'"+pasteTestImage(t)+"'\x1b[201~")
	terminalTest(u).Selection = &terminalsession.SourceSelection{Selection: selectedTerminalText("quote"), Mention: "message"}
	composerShellKeys(t, u, "rtyped")
	rows, _ := u.testMainFrame(80, 10, 0)
	uisnapshot.Assert(t, "testdata/snapshots/composer-paste-reference-scrollback.txt", strings.Join(rows, "\n")+"\n")
}
