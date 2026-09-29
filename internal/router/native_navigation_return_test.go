package router

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestNativeReplyDialogPreservesScrolledDiff(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	c := liveDiffChangesController(t, 120, 28, []livediff.Chunk{liveDiffCapture("a", "a.go", 1, "", liveDiffLinesFile("a", 80), livediff.Origin{Change: "amber1", Caller: "/root"})})
	u.shell.diff = c
	u.shell.side, u.shell.diffOpen, u.shell.focus = true, true, 1
	c.view.Following = false
	key := c.view.Files[0].Key()
	c.view.Scroll[key] = 12
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "ct", "item": map[string]any{"id": "reply", "type": "collabAgentToolCall", "tool": "sendMessage", "status": "completed", "senderThreadId": "child", "receiverThreadIds": []string{"main"}, "prompt": strings.Repeat("Reply body.\n", 16)}})
	if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
		t.Fatal(err)
	}
	var seq uint64
	for _, entry := range u.view.entries {
		if entry.activitySeq != 0 {
			seq = entry.Seq
		}
	}
	if seq == 0 || !u.shell.openActivityReply(seq) || u.shell.output == nil {
		t.Fatal("reply did not open dialog")
	}
	if !u.shell.diffOpen || u.shell.focus != 1 || c.view.Scroll[key] != 12 || c.view.Following {
		t.Fatal("opening reply changed underlying diff navigation")
	}
	if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.shell.output.laid.Text, "Reply body.") {
		t.Fatal("dialog lost selected reply")
	}
	u.shell.outputKey("\x1b")
	if u.shell.output != nil || !u.shell.diffOpen || u.shell.focus != 1 || c.view.Scroll[key] != 12 || c.view.Following {
		t.Fatal("dismissing reply changed underlying diff")
	}
}
