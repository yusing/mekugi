package router

import (
	"bytes"
	"strings"
	"testing"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotAppServerArchive(t *testing.T) {
	for _, running := range []bool{false, true} {
		name := "idle"
		if running {
			name = "running"
		}
		t.Run(name, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.source.Audience = 0 // The modal fixture retains the unconnected feed header.
			if running {
				u.Turn = "turn"
			}
			appServerTestKeys(t, u, "/arch\r")
			u.settlePresentation()
			if u.picker.Kind != "archive" || u.mainView.Menu.Selected != 0 || wire.Len() != 0 {
				t.Fatal("archive must first ask for confirmation, defaulting to No")
			}
			for _, width := range []int{40, 80} {
				rows, _ := u.testMainFrame(width, 12, 0)
				assertNativeUISnapshot(t, "archive-confirm-"+name+"-"+exitTokenCount(uint64(width)), rows)
			}
			appServerTestKeys(t, u, "\x1b[B\r")
			requests := restoreContentRequests(t, wire)
			if len(requests) != 1 || requests[0].Method != "thread/archive" || requests[0].Params["threadId"] != "main" || u.quitRequested {
				t.Fatalf("archive dispatch = %s, quit=%v", wire.String(), u.quitRequested)
			}
			u.conversation().Queued = []conversationmodel.Input{{Text: "pending follow-up"}}
			appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"turn","status":"completed"}}}`)
			appServerTestKeys(t, u, "/resume other\r\x03")
			if wire.Len() == 0 || len(pickerRequests(t, wire)) != 1 || u.AcceptsInput() || u.AcceptsShell() {
				t.Fatal("archive admitted input or a session switch before acknowledgement")
			}
			appServerTestMessage(t, u, `{"id":1,"result":{}}`)
			if !u.quitRequested || !u.archived || len(pickerRequests(t, wire)) != 1 || len(u.conversation().Queued) != 1 {
				t.Fatal("successful archive did not exit with waiting input retained")
			}
			var out bytes.Buffer
			if err := u.writeExitSummary(&out, false); err != nil {
				t.Fatal(err)
			}
			uisnapshot.Assert(t, "testdata/snapshots/native-exit-archived.txt", out.String())
		})
	}
}

func TestAppServerArchiveCancelAndUnavailable(t *testing.T) {
	for _, cancel := range []string{"\r", "\x03", "\x1b"} {
		u, wire := newAppServerTestUI()
		u.ensureShell()
		for _, key := range []byte("/archive\r\x1b[200~clipboard text\x1b[201~") {
			if err := terminalTest(u).key(key); err != nil {
				t.Fatal(err)
			}
		}
		u.pickerKey(cancel)
		u.refreshPicker()
		if wire.Len() != 0 || u.picker.Open || u.Replacement.Pending() || u.quitRequested || u.Text != "" {
			t.Fatalf("cancel %q archived, left confirmation open or accepted hidden paste: %q", cancel, u.Text)
		}
	}
	for _, state := range []string{"startup", "restoring", "replacement"} {
		u, wire := newAppServerTestUI()
		switch state {
		case "startup":
			u.Thread = ""
		case "restoring":
			u.restoring = new(appServerActivityRestore)
		case "replacement":
			u.Replacement.Clear()
		}
		appServerTestKeys(t, u, "/archive\r")
		if wire.Len() != 0 || u.picker.Kind == "archive" || !strings.Contains(u.feedback.Text, "ready") {
			t.Fatalf("archive allowed during %s", state)
		}
	}
}

func TestAppServerArchiveFailureRetry(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/archive\r\x1b[B\r")
	appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"archive unavailable"}}`)
	if u.quitRequested || u.archived || u.Replacement.Pending() || !u.feedback.Alert || !strings.Contains(u.feedback.Text, "archive unavailable") {
		t.Fatalf("archive failure lost session or feedback: %q", u.feedback.Text)
	}
	appServerTestKeys(t, u, "/archive\r\x1b[B\r")
	if len(pickerRequests(t, wire)) != 2 {
		t.Fatal("failed archive cannot be retried")
	}
	appServerTestMessage(t, u, `{"id":2,"result":{}}`)
	if !u.archived || !u.quitRequested {
		t.Fatal("retry failed to exit")
	}
}
