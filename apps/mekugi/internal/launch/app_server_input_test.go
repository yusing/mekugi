package launch

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestPasteAdmissionPreservesOriginalDestination(t *testing.T) {
	for _, change := range []string{"status", "draft", "menu", "prompt"} {
		t.Run(change, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			appServerTestKeys(t, u, "\x1b[200~payload")
			switch change {
			case "status":
				setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "new status", false)
			case "draft":
				u.Editor.Replace(composer.Draft{Text: "replacement"})
			case "menu":
				u.picker.openMenu("manage")
			case "prompt":
				questionTestSync(t, u, "replacement", false)
				openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
			}
			appServerTestKeys(t, u, "\x1b[201~")
			want := ""
			if change == "status" {
				want = "payload"
			} else if change == "draft" {
				want = "replacement"
			}
			if u.Text != want || u.picker.Query != "" || wire.Len() != 0 || u.shell.Main.Pasting() {
				t.Fatalf("paste retargeted: draft=%q, query=%q, wire=%q", u.Text, u.picker.Query, wire.String())
			}
			if change != "status" && !u.attached.feedback.Alert {
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
	if u.shell.Prompts.Secret.Text != "private" || u.attached.presentation.View().PromptDraft.Draft.Text != "" {
		t.Fatal("secret paste was lost or published")
	}
	appServerTestKeys(t, u, "\x1b[200~stale")
	u.shell.Prompts.Reopen()
	appServerTestKeys(t, u, "\x1b[201~")
	if u.shell.Prompts.Secret.Text != "private" || !u.attached.feedback.Alert {
		t.Fatal("reopened secret editor accepted an old paste")
	}
}

func TestPromptInputRejectsRetiredDock(t *testing.T) {
	u, wire := newAppServerTestUI()
	questionTestSync(t, u, "retired-dock", false)
	questionTestPaint(t, u, 60)
	input := u.shell.Main.PromptKey("x", u.shell.Prompts)
	if input == nil {
		t.Fatal("question did not produce targeted input")
	}
	u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.shell, u.Editor, u.Thread)
	publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	if handled, err := applyPromptInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, u.review, &u.pendingEditReceipts, u.notifications, *input); !handled || err != nil || !u.attached.feedback.Alert || u.Text != "" || wire.Len() != 0 {
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

			u.Turn = "active"
			if err := terminalTest(u).key(27); err != nil {
				t.Fatal(err)
			}
			// The UI stalled after consuming ESC; the suffix is already queued.
			ageTerminalEscape(terminalTest(u))
			keys := make(chan byte, len(tc.suffix))
			for _, key := range []byte(tc.suffix) {
				keys <- key
			}
			if err := u.drainKeys(keys); err != nil {
				t.Fatal(err)
			}
			if u.Text != tc.want || wire.Len() != 0 || terminalTest(u).Decoder.Pasting() {
				t.Fatalf("draft=%q want=%q wire=%q paste=%v", u.Text, tc.want, wire.String(), terminalTest(u).Decoder.Pasting())
			}
		})
	}
}

func TestNativeUIInputDrainPreservesStandaloneEscape(t *testing.T) {
	u, wire := newAppServerTestUI()

	u.Turn = "active"
	u.Text = "keep me"
	if err := terminalTest(u).key(27); err != nil {
		t.Fatal(err)
	}
	ageTerminalEscape(terminalTest(u))
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

		terminalTest(u).Focus = 1
		for _, key := range []byte("?\x1b") {
			if err := terminalTest(u).key(key); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(40 * time.Millisecond)
		if err := u.drainKeys(make(chan byte)); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(paintSavedDiff(t, savedPane(terminalTest(u)), 80, 24), "\n"), "Diff navigation") {
			t.Fatal("settled Escape did not close Diff help")
		}
		if err := terminalTest(u).key('?'); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(paintSavedDiff(t, savedPane(terminalTest(u)), 80, 24), "\n"), "Diff navigation") || wire.Len() != 0 {
			t.Fatalf("following key did not reopen Diff help: help=%v host=%q", strings.Contains(strings.Join(paintSavedDiff(t, savedPane(terminalTest(u)), 80, 24), "\n"), "Diff navigation"), wire.String())
		}
	})
}

func TestNativeUIInputDrainBoundedAndClosed(t *testing.T) {
	u, _ := newAppServerTestUI()

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
	if len(keys) != 2 || !terminalTest(u).Decoder.Pending() {
		t.Fatal("input budget did not preserve pending sequence")
	}
	ageTerminalEscape(terminalTest(u))
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
