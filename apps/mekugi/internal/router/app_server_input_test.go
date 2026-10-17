package router

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestPasteAdmissionPreservesOriginalDestination(t *testing.T) {
	for _, change := range []string{"status", "draft", "menu", "prompt"} {
		t.Run(change, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			appServerTestKeys(t, u, "\x1b[200~payload")
			switch change {
			case "status":
				u.setNotice("new status", false)
			case "draft":
				u.Editor.Replace(composer.Draft{Text: "replacement"})
			case "menu":
				u.openMenu("manage")
			case "prompt":
				questionTestSync(t, u, "replacement", false)
				u.openQuestions()
			}
			appServerTestKeys(t, u, "\x1b[201~")
			want := ""
			if change == "status" {
				want = "payload"
			} else if change == "draft" {
				want = "replacement"
			}
			if u.Text != want || u.picker.Query != "" || wire.Len() != 0 || u.mainView.Pasting() {
				t.Fatalf("paste retargeted: draft=%q, query=%q, wire=%q", u.Text, u.picker.Query, wire.String())
			}
			if change != "status" && !u.feedback.Alert {
				t.Fatal("rejected paste lacks feedback")
			}
		})
	}
}

func TestSecretPasteRejectsReopenedEditor(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Turn = "turn"
	questionTestSync(t, u, "secret-paste", true)
	questionTestPaint(t, u, 60)
	appServerTestKeys(t, u, "\x1b[200~private\x1b[201~")
	if u.prompts.Secret.Text != "private" || u.sessionPresentation().View().PromptDraft.Draft.Text != "" {
		t.Fatal("secret paste was lost or published")
	}
	appServerTestKeys(t, u, "\x1b[200~stale")
	u.prompts.Reopen()
	appServerTestKeys(t, u, "\x1b[201~")
	if u.prompts.Secret.Text != "private" || !u.feedback.Alert {
		t.Fatal("reopened secret editor accepted an old paste")
	}
}

func TestPromptInputRejectsRetiredDock(t *testing.T) {
	u, wire := newAppServerTestUI()
	questionTestSync(t, u, "retired-dock", false)
	questionTestPaint(t, u, 60)
	input := u.mainView.PromptKey("x", &u.prompts)
	if input == nil {
		t.Fatal("question did not produce targeted input")
	}
	u.hideQuestions()
	u.publishPresentation(u.now())
	if handled, err := u.applyPromptInput(*input); !handled || err != nil || !u.feedback.Alert || u.Text != "" || wire.Len() != 0 {
		t.Fatalf("retired prompt input escaped admission: handled=%v err=%v draft=%q", handled, err, u.Text)
	}
}

func TestNativeUIQueuedEscapeContinuation(t *testing.T) {
	for _, tc := range []struct{ name, suffix, want string }{
		{"mouse", "[<35;4;30M\x1b[<35;21;31M", ""},
		{"paste", "[200~hello\x1b[201~", "hello"},
		{"paste-end", "[201~", ""},
		{"arrow", "[D", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.ensureShell()
			u.Turn = "active"
			if err := u.shell.key(27); err != nil {
				t.Fatal(err)
			}
			// The UI stalled after consuming ESC; the suffix is already queued.
			ageTerminalEscape(u.shell)
			keys := make(chan byte, len(tc.suffix))
			for _, key := range []byte(tc.suffix) {
				keys <- key
			}
			if err := u.drainKeys(keys); err != nil {
				t.Fatal(err)
			}
			if u.Text != tc.want || wire.Len() != 0 || u.shell.Decoder.Pasting() {
				t.Fatalf("draft=%q want=%q wire=%q paste=%v", u.Text, tc.want, wire.String(), u.shell.Decoder.Pasting())
			}
		})
	}
}

func TestNativeUIInputDrainPreservesStandaloneEscape(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ensureShell()
	u.Turn = "active"
	u.Text = "keep me"
	if err := u.shell.key(27); err != nil {
		t.Fatal(err)
	}
	ageTerminalEscape(u.shell)
	if err := u.drainKeys(make(chan byte, 64)); err != nil {
		t.Fatal(err)
	}
	appServerOneRequest(t, wire, "turn/interrupt", "")
	if u.Text != "keep me" || u.Interruption.Target != "active" {
		t.Fatal("standalone Escape did not preserve interrupt behavior")
	}
}

func TestNativeUIDiffStandaloneEscapeBeforeNextKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u, wire := newAppServerTestUI()
		u.ensureShell()

		u.shell.Focus = 1
		for _, key := range []byte("?\x1b") {
			if err := u.shell.key(key); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(40 * time.Millisecond)
		if err := u.drainKeys(make(chan byte)); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(paintSavedDiff(t, savedPane(u.shell), 80, 24), "\n"), "Diff navigation") {
			t.Fatal("settled Escape did not close Diff help")
		}
		if err := u.shell.key('?'); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(paintSavedDiff(t, savedPane(u.shell), 80, 24), "\n"), "Diff navigation") || wire.Len() != 0 {
			t.Fatalf("following key did not reopen Diff help: help=%v host=%q", strings.Contains(strings.Join(paintSavedDiff(t, savedPane(u.shell), 80, 24), "\n"), "Diff navigation"), wire.String())
		}
	})
}

func TestNativeUIInputDrainBoundedAndClosed(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	keys := make(chan byte, 258)
	for range 255 {
		keys <- 'a'
	}
	keys <- 27
	keys <- '['
	keys <- 'D'
	if err := u.drainKeys(keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || !u.shell.Decoder.Pending() {
		t.Fatal("input budget did not preserve pending sequence")
	}
	ageTerminalEscape(u.shell)
	if err := u.drainKeys(keys); err != nil {
		t.Fatal(err)
	}
	if u.Text != strings.Repeat("a", 255) || u.CursorBack != 1 {
		t.Fatal("sequence crossing drain budget was corrupted")
	}
	close(keys)
	if err := u.drainKeys(keys); !errors.Is(err, io.EOF) {
		t.Fatalf("closed input: %v", err)
	}
}
