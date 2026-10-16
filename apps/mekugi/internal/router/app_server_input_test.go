package router

import (
	"errors"
	"io"
	"strings"
	"testing"
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
			u.turn = "active"
			if err := u.shell.key(27); err != nil {
				t.Fatal(err)
			}
			// The UI stalled after consuming ESC; the suffix is already queued.
			u.shell.sequenceAt = time.Now().Add(-time.Second)
			keys := make(chan byte, len(tc.suffix))
			for _, key := range []byte(tc.suffix) {
				keys <- key
			}
			if err := u.drainKeys(keys); err != nil {
				t.Fatal(err)
			}
			if u.draft != tc.want || wire.Len() != 0 || u.shell.paste || u.paste {
				t.Fatalf("draft=%q want=%q wire=%q paste=%v/%v", u.draft, tc.want, wire.String(), u.shell.paste, u.paste)
			}
		})
	}
}

func TestNativeUIInputDrainPreservesStandaloneEscape(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ensureShell()
	u.turn = "active"
	u.draft = "keep me"
	if err := u.shell.key(27); err != nil {
		t.Fatal(err)
	}
	u.shell.sequenceAt = time.Now().Add(-time.Second)
	if err := u.drainKeys(make(chan byte, 64)); err != nil {
		t.Fatal(err)
	}
	appServerOneRequest(t, wire, "turn/interrupt", "")
	if u.draft != "keep me" || u.interruption.target != "active" {
		t.Fatal("standalone Escape did not preserve interrupt behavior")
	}
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
	if len(keys) != 2 || u.shell.sequence != "\x1b" {
		t.Fatal("input budget did not preserve pending sequence")
	}
	u.shell.sequenceAt = time.Now().Add(-time.Second)
	if err := u.drainKeys(keys); err != nil {
		t.Fatal(err)
	}
	if u.draft != strings.Repeat("a", 255) || u.cursorBack != 1 {
		t.Fatal("sequence crossing drain budget was corrupted")
	}
	close(keys)
	if err := u.drainKeys(keys); !errors.Is(err, io.EOF) {
		t.Fatalf("closed input: %v", err)
	}
}
