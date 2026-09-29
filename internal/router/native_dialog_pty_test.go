package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestNativeDialogPTYCloseAndResize(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	frames := make(chan []byte, 4)
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
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: "Underlying conversation"}}})
	screen := vt.NewEmulator(120, 32)
	defer screen.Close()
	paint := func(width, height int) string {
		t.Helper()
		if err := pty.Setsize(master, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)}); err != nil {
			t.Fatal(err)
		}
		screen.Resize(width, height)
		if err := u.paint(slave, width, height); err != nil {
			t.Fatal(err)
		}
		select {
		case frame := <-frames:
			if _, err := screen.Write(frame); err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("PTY frame timeout")
		}
		return screen.String()
	}
	paint(120, 32)
	pages := []activityui.Block{{Kind: "op", Verb: "Run", Code: "mchanges amber1", Tail: strings.Split("amber1\n--- a.go\n+++ a.go\n@@ -1 +1 @@\n-old\n+new", "\n")}}
	u.shell.openBlocks(u.view, pages)
	for _, size := range [][2]int{{120, 32}, {48, 20}, {160, 40}} {
		frame := paint(size[0], size[1])
		if !strings.Contains(frame, "[×]") || !strings.Contains(frame, "+new") {
			t.Fatalf("%v dialog missing from PTY:\n%s", size, frame)
		}
	}
	rect := u.shell.output.rect
	for _, end := range []string{"M", "m"} {
		if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", rect.x+rect.w-5, rect.y+1, end)); err != nil {
			t.Fatal(err)
		}
	}
	frame := paint(160, 40)
	if u.shell.output != nil || strings.Contains(frame, "[×]") || !strings.Contains(frame, "Underlying conversation") {
		t.Fatalf("close lost original pane:\n%s", frame)
	}
}
