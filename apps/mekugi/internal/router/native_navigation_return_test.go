package router

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestNativeReplyDialogPreservesScrolledDiff(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	c := liveDiffChangesPane(t, 120, 28, []presentation.Chunk{liveDiffCapture("a", "a.go", 1, "", liveDiffLinesFile("a", 80), presentation.Origin{Change: "amber1", Caller: "/root"})})
	u.shell.diff = c
	u.shell.DiffOpen, u.shell.Focus = true, 1

	for range 12 {
		c.Key("j")
	}
	paintSavedDiff(t, c, 120, 28)
	offset := c.Selection(119, 27).Offset
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "ct", "item": map[string]any{"id": "reply", "type": "collabAgentToolCall", "tool": "sendMessage", "status": "completed", "senderThreadId": "child", "receiverThreadIds": []string{"main"}, "prompt": strings.Repeat("Reply body.\n", 16)}})
	if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
		t.Fatal(err)
	}
	var seq uint64
	for _, entry := range u.view.Entries {
		if entry.Source.activitySeq != 0 {
			seq = entry.Source.Seq
		}
	}
	if seq == 0 || !u.shell.openActivityReply(seq) || u.shell.output == nil {
		t.Fatal("reply did not open dialog")
	}
	if !u.shell.DiffOpen || u.shell.Focus != 1 || c.Selection(u.shell.layout.Diff.W-1, u.shell.layout.Diff.H-1).Offset != offset {
		t.Fatal("opening reply changed underlying diff navigation")
	}
	if err := u.paint(&bytes.Buffer{}, 120, 28); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.shell.output.Document.Text, "Reply body.") {
		t.Fatal("dialog lost selected reply")
	}
	u.shell.outputKey("\x1b")
	if u.shell.output != nil || !u.shell.DiffOpen || u.shell.Focus != 1 || c.Selection(u.shell.layout.Diff.W-1, u.shell.layout.Diff.H-1).Offset != offset {
		t.Fatal("dismissing reply changed underlying diff")
	}
}
