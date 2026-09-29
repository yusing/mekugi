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

func TestNativeNavigationReturnBounded(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir()).shell
	for i := range nativeNavigationReturnLimit * 3 {
		u.main.view.offset = i
		u.pushNavigationReturn()
		if len(u.navigationReturns) > nativeNavigationReturnLimit {
			t.Fatal("unbounded return history")
		}
	}
	for i := nativeNavigationReturnLimit*3 - 1; i >= nativeNavigationReturnLimit*2; i-- {
		if !u.popNavigationReturn() || u.main.view.offset != i {
			t.Fatalf("return order at %d: %d", i, u.main.view.offset)
		}
	}
	if u.popNavigationReturn() {
		t.Fatal("discarded history returned")
	}
	for _, b := range u.navigationReturns[:cap(u.navigationReturns)] {
		if b.diff.scroll != nil || b.diff.editPreview != nil {
			t.Fatal("popped state retained")
		}
	}
}

func TestNativeNavigationReturnPreservesCurrentPane(t *testing.T) {
	for _, diffOpen := range []bool{false, true} {
		for focus := range 4 {
			t.Run(fmt.Sprint(diffOpen, focus), func(t *testing.T) {
				u := newAppServerSessionTestUI(t, t.TempDir()).shell
				u.diffOpen, u.focus = diffOpen, (focus+1)%4
				u.pushNavigationReturn()
				u.diffOpen, u.focus = !diffOpen, focus
				if !u.popNavigationReturn() {
					t.Fatal("missing return")
				}
				want := focus
				if focus == 1 || focus == 2 {
					want = 2
					if diffOpen {
						want = 1
					}
				}
				if u.focus != want {
					t.Fatalf("focus = %d, want %d", u.focus, want)
				}
			})
		}
	}
}

func TestNativeReplyPreviewSameDestination(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir()).shell
	u.main.view.entries = []activityPaneEntry{{Seq: 1, activitySeq: 2}}
	u.agents.entries = []activityPaneEntry{{Seq: 2, Agent: "/root/worker"}}
	if !u.openActivityReply(1) || !u.openActivityReply(1) || len(u.navigationReturns) != 1 {
		t.Fatal("pending destination added duplicate history")
	}
	// Once rendered, both ordinary and bottom-clamped targets remain no-ops.
	for _, row := range []int{8, 100} {
		u.agents.pendingTarget = 0
		u.agents.questionRows = map[uint64]int{2: row}
		u.agents.feedLines, u.agents.feedRows = 30, 10
		u.agents.offset, u.agents.following = min(row-1, 20), false
		if !u.openActivityReply(1) || len(u.navigationReturns) != 1 {
			t.Fatal("rendered destination added duplicate history")
		}
	}
	u.agents.offset = 0
	if !u.openActivityReply(1) || len(u.navigationReturns) != 2 {
		t.Fatal("different scroll position lost return")
	}
}

func TestNativeQuestionPreviewSameDestination(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir()).shell
	v := u.main.view
	v.feedOnly, v.conversation = true, true
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{
		{Seq: 1, Agent: "Main", Kind: "text", Text: strings.Repeat("Earlier reply.\n", 30)},
		{Seq: 2, Agent: "You", Kind: "text", Text: "Bottom question"},
	}})
	v.render(80, 10, time.Now())
	initialRows := v.feedRows
	for range 2 {
		u.selection = &terminalSelection{rect: terminalRect{w: 10, h: 1}, dragging: true, view: v, rows: []string{"question"}, questions: []uint64{2}}
		if !u.selectionMouse(0, 0, 0, true) {
			t.Fatal("question click not consumed")
		}
		v.render(80, 10, time.Now())
	}
	if len(u.navigationReturns) != 1 || v.offset != v.feedLines-v.feedRows || v.feedRows != initialRows-1 {
		t.Fatalf("duplicate question return: %d, offset %d, rows %d -> %d", len(u.navigationReturns), v.offset, initialRows, v.feedRows)
	}
}
