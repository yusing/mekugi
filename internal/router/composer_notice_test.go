package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestComposerErrorClickAndDismiss(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		width int
	}{
		{"command", 12}, {"command", 13}, {"command", 60}, {"attachment", 60}, {"host", 60},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.kind, tc.width), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.ensureShell()
			defer u.shell.diffScreen.Close()
			u.shell.side = false
			u.turn, u.status, u.draft = "active", "Working", "keep draft"
			switch tc.kind {
			case "command":
				u.draft = "/unknown"
				appServerTestKeys(t, u, "\r")
			case "attachment":
				d := composerDraft{files: []composerFile{{path: "/missing/setup.sh"}}}
				d.snapshotFileAttachments("/missing")
				u.setNotice(d.attachmentNotice, true)
				frames, ok := decodeFileAttachments(d.attachments[0])
				if !ok || strings.Contains(strings.Join(frames, "\n"), d.attachmentNotice) || !strings.Contains(strings.Join(frames, "\n"), "CONTENT NOT ATTACHED") {
					t.Fatal("UI warning leaked into the model attachment result")
				}
			case "host":
				appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"active","status":"failed","error":{"message":"provider failed\nFull diagnostics"}}}}`)
			}
			full, draft, turn, sent := u.composerErrorText(), u.draft, u.turn, wire.String()
			screen := vt.NewEmulator(tc.width+2, 16)
			defer screen.Close()
			if err := u.paint(screen, tc.width+2, 16); err != nil {
				t.Fatal(err)
			}
			click := func(rect terminalRect) {
				t.Helper()
				if rect.w == 0 {
					t.Fatal("missing error control")
				}
				x, y := rect.x+u.shell.layout.codex.x+1, rect.y+u.shell.layout.codex.y+1
				if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%dM", x, y)); err != nil {
					t.Fatal(err)
				}
			}
			click(u.noticeDetails)
			if u.shell.output == nil {
				t.Fatal("truncated error did not open")
			}
			drawOutputDialog(u.shell)
			if u.shell.output.laid.Text != livediff.Safe(full, false) {
				t.Fatal("details lost full error")
			}
			u.shell.outputKey("q")
			click(u.noticeDismiss)
			if u.composerErrorText() != "" || u.draft != draft || u.turn != turn || wire.String() != sent || len(u.view.entries) != 0 {
				t.Fatal("dismissal changed draft, host state, wire, or transcript")
			}
		})
	}
}

func TestUISnapshotComposerError(t *testing.T) {
	for _, width := range []int{12, 20, 36, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.view.painter.Theme = livediff.DarkTheme
			u.clock = func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }
			u.status = "Ready"
			u.setNotice("File contents omitted: setup.sh (attachment budget exceeded).\nThe attachment result names the missing content.", true)
			rows, _ := u.mainFrame(width, 8, 0)
			assertNativeUISnapshot(t, fmt.Sprintf("composer-error-%d", width), rows)
			if !strings.Contains(rows[u.noticeDismiss.y], activityui.Green) || !strings.Contains(ansi.Strip(rows[u.noticeDismiss.y]), "[✓]") {
				t.Fatal("dismiss check is missing its shared success color")
			}
		})
	}
	u, _ := newAppServerTestUI()
	u.setNotice("failed", true)
	u.mainFrame(80, 8, 0)
	u.setNotice("done", false)
	u.mainFrame(80, 8, 0)
	if u.noticeDetails.w != 0 || u.noticeDismiss.w != 0 {
		t.Fatal("stale error controls survived feedback")
	}
}
