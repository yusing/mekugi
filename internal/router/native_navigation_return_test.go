package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestNativeReplyPreviewReturnsToScrolledDiff(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Cols: 160, Rows: 48}); err != nil {
		t.Fatal(err)
	}
	frames := make(chan []byte, 1)
	go func() {
		var pending []byte
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			pending = append(pending, buf[:n]...)
			for {
				end := bytes.Index(pending, []byte("\x1b[?2026l"))
				if end < 0 {
					break
				}
				end += len("\x1b[?2026l")
				frames <- append([]byte(nil), pending[:end]...)
				pending = pending[end:]
			}
			if err != nil {
				return
			}
		}
	}()
	screen := vt.NewEmulator(160, 48)
	defer screen.Close()
	u := newAppServerSessionTestUI(t, t.TempDir())
	c := liveDiffChangesController(t, 160, 48, []livediff.Chunk{liveDiffCapture("a", "a.go", 1, "", liveDiffLinesFile("a", 80), livediff.Origin{Change: "amber1", Caller: "/root"})})
	u.shell.diff = c
	u.shell.side, u.shell.diffOpen = true, true
	c.view.Following = false
	key := c.view.Files[0].Key()
	c.view.Scroll[key] = 12
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "ct", "item": map[string]any{"id": "reply", "type": "collabAgentToolCall", "tool": "sendMessage", "status": "completed", "senderThreadId": "child", "receiverThreadIds": []string{"main"}, "prompt": strings.Repeat("Reply body.\n", 16)}})
	paint := func() {
		t.Helper()
		if err := u.paint(slave, 160, 48); err != nil {
			t.Fatal(err)
		}
		select {
		case frame := <-frames:
			if _, err := screen.Write(frame); err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("preview PTY frame did not complete")
		}
	}
	paint()
	saved := c.view.Scroll[key]
	if saved == 0 {
		t.Fatal("diff was not scrolled")
	}
	clicked := false
	for row, seq := range u.view.feedQuestions {
		if seq == 0 {
			continue
		}
		x, y := u.shell.layout.codex.x+u.view.feedLeft+1, u.shell.layout.codex.y+u.view.feedTop+row
		for _, end := range []string{"M", "m"} {
			if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", x, y, end)); err != nil {
				t.Fatal(err)
			}
		}
		clicked = true
		break
	}
	if !clicked || u.shell.diffOpen || u.shell.focus != 2 {
		t.Fatal("reply did not open Activity")
	}
	paint()
	// Like Main's “Back to bottom”, the hint sits centered on the previewed
	// pane's bottom row rather than in the status bar.
	lines := strings.Split(screen.String(), "\n")
	pane := u.shell.layout.agents
	if hint := lines[pane.y+pane.h-1]; !strings.Contains(hint, "↩ Back to previous view · esc") {
		t.Fatalf("return hint missing from Activity's bottom row: %q", hint)
	}
	if strings.Contains(lines[len(lines)-1], "previous view") {
		t.Fatal("return hint stayed in the status bar")
	}
	// A nested roster click must restore the outer reply preview first.
	before := u.agents.offset
	only := u.agents.only
	clicked = false
	for _, hit := range u.agents.hits {
		if hit.agent != "/root/worker" {
			continue
		}
		r := u.shell.layout.roster
		for _, end := range []string{"M", "m"} {
			if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", r.x+hit.first, r.y+hit.row, end)); err != nil {
				t.Fatal(err)
			}
		}
		clicked = true
		break
	}
	if !clicked || u.agents.only == only {
		t.Fatal("nested roster click did not navigate")
	}
	escape := func() {
		t.Helper()
		if err := u.shell.key(27); err != nil {
			t.Fatal(err)
		}
		u.shell.sequenceAt = time.Now().Add(-time.Second)
		if err := u.shell.flushEscape(); err != nil {
			t.Fatal(err)
		}
	}
	escape()
	if u.shell.diffOpen || u.agents.offset != before || u.agents.only != only {
		t.Fatal("nested preview did not restore Activity")
	}
	escape()
	if !u.shell.diffOpen || !u.shell.side || c.view.Scroll[key] != saved || c.view.Following {
		t.Fatalf("lost previous diff state: scroll=%d want=%d", c.view.Scroll[key], saved)
	}
	paint()
	if c.view.Scroll[key] != saved {
		t.Fatal("paint lost restored scroll")
	}
	if strings.Contains(screen.String(), "previous view") {
		t.Fatal("return hint survived final return")
	}
	// A previewed diff gives up its bottom row to the same hint.
	rows := u.shell.layout.diff.h
	u.shell.pushNavigationReturn()
	paint()
	lines = strings.Split(screen.String(), "\n")
	pane = u.shell.layout.diff
	if hint := lines[pane.y+pane.h]; pane.h != rows-1 || !strings.Contains(hint, "↩ Back to previous view · esc") {
		t.Fatalf("return hint missing below the previewed diff (%d of %d rows): %q", pane.h, rows, hint)
	}
}
