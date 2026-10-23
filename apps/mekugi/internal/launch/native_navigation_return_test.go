package launch

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestNativeReplyDialogPreservesScrolledDiff(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	c := liveDiffChangesPane(t, 120, 28, []presentation.Chunk{liveDiffCapture("a", "a.go", 1, "", liveDiffLinesFile("a", 80), presentation.Origin{Change: "amber1", Caller: "/root"})})
	terminalTest(u).Auxiliary = c
	terminalTest(u).DiffOpen, terminalTest(u).Focus = true, 1

	for range 12 {
		c.Key("j")
	}
	paintSavedDiff(t, c, 120, 28)
	offset := c.Selection(119, 27).Offset
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "ct", "item": coordinationTestCall(t, "reply", "send_message", "main", u.attached.session.Caller("main"), "## Summary\nReply body.\n## Details\n"+strings.Repeat("Reply body.\n", 16))})
	if err := u.testPaint(&bytes.Buffer{}, 120, 28); err != nil {
		t.Fatal(err)
	}
	var seq uint64
	for _, entry := range u.attached.source.Entries {
		if entry.Source.ActivitySeq != 0 {
			seq = entry.Source.Seq
		}
	}
	if seq == 0 || !terminalTest(u).openActivityReply(seq) || terminalTest(u).Dialog == nil {
		t.Fatal("reply did not open dialog")
	}
	if !terminalTest(u).DiffOpen || terminalTest(u).Focus != 1 || c.Selection(terminalTest(u).Layout.Diff.W-1, terminalTest(u).Layout.Diff.H-1).Offset != offset {
		t.Fatal("opening reply changed underlying diff navigation")
	}
	if err := u.testPaint(&bytes.Buffer{}, 120, 28); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(terminalTest(u).Dialog.Document.Text, "Reply body.") {
		t.Fatal("dialog lost selected reply")
	}
	terminalTest(u).outputKey("\x1b")
	if terminalTest(u).Dialog != nil || !terminalTest(u).DiffOpen || terminalTest(u).Focus != 1 || c.Selection(terminalTest(u).Layout.Diff.W-1, terminalTest(u).Layout.Diff.H-1).Offset != offset {
		t.Fatal("dismissing reply changed underlying diff")
	}
}
