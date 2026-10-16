package router

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

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
			if u.Text != tc.want || wire.Len() != 0 || u.shell.input.Pasting() || u.paste {
				t.Fatalf("draft=%q want=%q wire=%q paste=%v/%v", u.Text, tc.want, wire.String(), u.shell.input.Pasting(), u.paste)
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
		defer u.shell.diff.close()
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
		if u.shell.diff.help {
			t.Fatal("settled Escape did not close Diff help")
		}
		if err := u.shell.key('?'); err != nil {
			t.Fatal(err)
		}
		if !u.shell.diff.help || wire.Len() != 0 {
			t.Fatalf("following key did not reopen Diff help: help=%v host=%q", u.shell.diff.help, wire.String())
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
	if len(keys) != 2 || !u.shell.input.Pending() {
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
