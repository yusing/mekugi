package router

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

func TestNativeControlsTerminalFrames(t *testing.T) {
	const width, height = 120, 35
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: height, Cols: width}); err != nil {
		t.Fatal(err)
	}
	frames := make(chan []byte, 4)
	go func() {
		var pending synchronizedFrameBuffer
		buffer := make([]byte, 8192)
		for {
			n, err := master.Read(buffer)
			if err != nil {
				return
			}
			pending.Append(buffer[:n])
			if frame, ok := pending.Next(); ok {
				frames <- bytes.Clone(frame)
			}
		}
	}()
	screen := vt.NewEmulator(width, height)
	defer screen.Close()
	u := newAppServerSessionTestUI(t, t.TempDir())
	paint := func() string {
		t.Helper()
		finishPacing(u.view, u.agents)
		if err := u.paint(slave, width, height); err != nil {
			t.Fatal(err)
		}
		select {
		case frame := <-frames:
			if _, err := screen.Write(frame); err != nil {
				t.Fatal(err)
			}
			return screen.String()
		case <-time.After(2 * time.Second):
			t.Fatal("terminal frame did not finish")
			return ""
		}
	}
	if frame := paint(); !strings.Contains(frame, "5 Journal ─") || !strings.Contains(frame, "No journal entries") {
		t.Fatalf("initial frame lacks Journal:\n%s", frame)
	}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
		"id": "spawn", "type": "subAgentActivity", "agentThreadId": "child", "agentPath": "/root/worker", "kind": "started"}})
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "child", "turn": map[string]any{"id": "child-turn"}})
	if frame := paint(); !strings.Contains(frame, "3 Activity ─") || strings.Contains(frame, "5 Journal ─") {
		t.Fatalf("spawn did not render Activity:\n%s", frame)
	}
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "child-turn", "status": "failed"}})
	if frame := paint(); !strings.Contains(frame, "5 Journal ─") || strings.Contains(frame, "3 Activity ─") {
		t.Fatalf("failed child did not restore Journal:\n%s", frame)
	}
	appServerTestKeys(t, u, "/lock\r")
	if frame := paint(); !strings.Contains(frame, "Locked · /unlock") {
		t.Fatalf("lock is not visible:\n%s", frame)
	}
	wire := u.client.Input.(*appServerTestInput)
	appServerOneRequest(t, wire, "thread/read", "") // Child metadata from the earlier spawn.
	appServerTestKeys(t, u, "/unlock\r")
	appServerTestTurn(t, u, "work")
	appServerTestKeys(t, u, "/compact\t")
	if frame := paint(); !strings.Contains(frame, "Compaction queued after this turn") {
		t.Fatalf("queued compaction is not visible:\n%s", frame)
	}
	if err := u.shell.key(3); err != nil {
		t.Fatal(err)
	}
	if frame := paint(); !strings.Contains(frame, "Queued compaction cancelled") || !strings.Contains(frame, "❯ /compact") || wire.Len() != 0 || u.turn != "work" {
		t.Fatalf("compact cancellation lost its draft or interrupted Main: wire=%q\n%s", wire.String(), frame)
	}
	appServerTestTurnEnd(t, u, "work", "completed")
	if wire.Len() != 0 {
		t.Fatalf("cancelled compaction ran after Main finished: %s", wire.String())
	}
}
