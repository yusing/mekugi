package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/uisnapshot"
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
	u.turn = "active"
	u.view.following = false
	composerShellKeys(t, u, "\x1b[200~'"+image+"'\x1b[201~")
	if u.paste || u.shell.paste || len(u.images) != 1 || u.draft != "[Image 1] " || u.view.following {
		t.Fatalf("paste on scrollback: draft=%q image=%v paste=%v/%v following=%v", u.draft, u.images, u.paste, u.shell.paste, u.view.following)
	}
	u.shell.selection = &terminalSelection{rect: terminalRect{0, 0, 5, 1}, rows: []string{"quote"}, endX: 4, moved: true, mention: "message"}
	composerShellKeys(t, u, "rtyped")
	if u.draft != "[Image 1] [Selected message] typed" || len(u.selections) != 1 || u.selections[0].text != "quote" {
		t.Fatalf("reference or typing frozen: %q %+v", u.draft, u.selections)
	}
	composerShellKeys(t, u, "\x1b[200~'"+image+"'\x1b[201~")
	if len(u.images) != 2 || strings.Contains(u.draft, "[201~") || strings.Contains(u.draft, image) {
		t.Fatalf("second paste leaked paths or markers: %q", u.draft)
	}
	composerShellKeys(t, u, "\x03")
	if u.draft != "" || wire.Len() != 0 {
		t.Fatalf("Ctrl-C did not clear draft: draft=%q wire=%q", u.draft, wire.String())
	}
	// First Escape returns scrollback to the bottom; the next interrupts.
	for range 2 {
		composerShellKeys(t, u, "\x1b")
		u.shell.sequenceAt = time.Now().Add(-time.Second)
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
	u.shell.selection = &terminalSelection{moved: true}
	composerShellKeys(t, u, "\x1b[201~typed")
	if u.draft != "payloadtyped" || u.paste || u.shell.paste || wire.Len() != 0 {
		t.Fatalf("selection consumed paste boundary: draft=%q paste=%v/%v", u.draft, u.paste, u.shell.paste)
	}
}

func TestUISnapshotComposerPasteAndReferenceOnScrollback(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.view.following = false
	composerShellKeys(t, u, "\x1b[200~'"+pasteTestImage(t)+"'\x1b[201~")
	u.shell.selection = &terminalSelection{rect: terminalRect{0, 0, 5, 1}, rows: []string{"quote"}, endX: 4, moved: true, mention: "message"}
	composerShellKeys(t, u, "rtyped")
	rows, _ := u.mainFrame(80, 10, 0)
	uisnapshot.Assert(t, "testdata/snapshots/composer-paste-reference-scrollback.txt", strings.Join(rows, "\n")+"\n")
}
