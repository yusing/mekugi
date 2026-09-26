package router

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

func TestTerminalUIIncrementalPaint(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.draft = "old draft text"
	screen := vt.NewEmulator(120, 30)
	defer screen.Close()
	var wire bytes.Buffer
	out := io.MultiWriter(screen, &wire)
	if err := u.paint(out, 120, 30); err != nil {
		t.Fatal(err)
	}
	wire.Reset()
	if err := u.paint(out, 120, 30); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wire.String(), "\x1b[2K") {
		t.Fatal("unchanged frame erased terminal rows")
	}
	u.draft = "new"
	wire.Reset()
	if err := u.paint(out, 120, 30); err != nil {
		t.Fatal(err)
	}
	if cleared := strings.Count(wire.String(), "\x1b[2K"); cleared != 1 {
		t.Fatalf("single-line input edit repainted %d rows", cleared)
	}
	if strings.Contains(screen.String(), "old draft text") || !strings.Contains(screen.String(), "new") {
		t.Fatal("incremental input update retained stale text")
	}
	// Resize and close the auxiliary panes; removed content must be cleared.
	u.shell.side = false
	screen.Resize(80, 20)
	if err := u.paint(screen, 80, 20); err != nil {
		t.Fatal(err)
	}
	fresh, _ := newAppServerTestUI()
	fresh.ensureShell()
	fresh.shell.side, fresh.draft = false, "new"
	want := vt.NewEmulator(80, 20)
	defer want.Close()
	if err := fresh.paint(want, 80, 20); err != nil {
		t.Fatal(err)
	}
	if screen.String() != want.String() {
		t.Fatal("incremental resize differs from a fresh terminal frame")
	}
}

func TestPaneScrollUnified(t *testing.T) {
	for _, key := range []byte{'j', 'k', ' ', 'b', 'g', 'G', 'r'} {
		next, follow, ok := paneScroll(key, 50, 10, 100)
		if !ok {
			t.Fatal("missing scroll binding")
		}
		v := newLiveActivityView()
		v.following = false
		v.offset = 50
		v.feedRows = 10
		v.feedLines = 100
		v.handleKey("", key)
		if v.offset != next || v.following != follow {
			t.Fatalf("agents %c: %d %v, expected %d %v", key, v.offset, v.following, next, follow)
		}
	}
	v := newLiveActivityView()
	v.feedRows = 10
	v.feedLines = 100
	v.handleMouse('k', 2, 2)
	if v.offset != 87 || v.following {
		t.Fatalf("wheel did not pause/scroll: %+v", v)
	}
	for _, seq := range []string{"\x1b[H", "\x1b[1~", "\x1bOH"} {
		var escape string
		for _, b := range []byte(seq) {
			escape, _ = v.handleKey(escape, b)
		}
		if v.offset != 0 {
			t.Fatalf("Home %q: %d", seq, v.offset)
		}
	}
	var escape string
	for _, b := range []byte("\x1b[F") {
		escape, _ = v.handleKey(escape, b)
	}
	if v.offset != 90 || v.following {
		t.Fatal("End must scroll to bottom and stay paused")
	}

}
