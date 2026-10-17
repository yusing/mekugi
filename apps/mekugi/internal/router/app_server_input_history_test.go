package router

import (
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestAppServerInputHistoryAcknowledgedAndDraftRestored(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "first\r")
	appServerTestMessage(t, u, `{"id":1,"result":{}}`)
	u.AwaitingTurn = false
	appServerTestKeys(t, u, "second\r")
	appServerTestMessage(t, u, `{"id":2,"result":{}}`)
	u.AwaitingTurn = false
	appServerTestKeys(t, u, "draft\x1b[D\x1b[A")
	if u.Text != "second" {
		t.Fatalf("newest = %q", u.Text)
	}
	appServerTestKeys(t, u, "\x1b[A")
	if u.Text != "first" {
		t.Fatalf("oldest = %q", u.Text)
	}
	appServerTestKeys(t, u, "\x1b[A\x1b[B\x1b[B")
	if u.Text != "draft" || u.CursorBack != 1 {
		t.Fatalf("draft = %q cursorBack=%d", u.Text, u.CursorBack)
	}
}

func TestAppServerInputHistoryMultilineBoundaryAndUndo(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.rememberInput(conversationmodel.Input{Text: "old"})
	appServerTestKeys(t, u, "one\ntwo\x1b[A")
	if u.Text != "one\ntwo" || u.Cursor() != 3 {
		t.Fatalf("up moved into history before first row: %q at=%d", u.Text, u.Cursor())
	}
	appServerTestKeys(t, u, "\x1b[A")
	if u.Text != "old" {
		t.Fatalf("history=%q", u.Text)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.Text != "one\ntwo" {
		t.Fatalf("undo recall=%q", u.Text)
	}
}

func TestAppServerInputHistoryAttachmentsRemainUsable(t *testing.T) {
	u, _ := newAppServerTestUI()
	path := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	u.rememberInput(conversationmodel.Input{Text: "old"})
	u.attachImage(path)
	appServerTestKeys(t, u, "\x1b[A")
	u.UndoDrafts = nil
	u.pruneDraftImages()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live draft image pruned: %v", err)
	}
	appServerTestKeys(t, u, "\x1b[B")
	if len(u.Images) != 1 || u.Images[0].Path != path {
		t.Fatalf("restored images=%+v", u.Images)
	}
	appServerTestKeys(t, u, "\r")
	appServerTestMessage(t, u, `{"id":1,"result":{}}`)
	appServerTestKeys(t, u, "\x1b[A")
	if len(u.Images) != 1 || u.Images[0].Path != path {
		t.Fatalf("history images=%+v", u.Images)
	}
}

func TestAppServerInputHistoryResumeAndRejectedInput(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.agents = activityui.NewView()
	u.session.start(u.Thread, "/workspace")
	u.restoreHistory([]binding.HistoryTurn{{ID: "old", Status: "completed", Items: []binding.ThreadItem{{Type: "userMessage", ID: "input", Content: []byte(`[{"type":"text","text":"saved"}]`)}}}})
	appServerTestKeys(t, u, "\x1b[A")
	if u.Text != "saved" {
		t.Fatalf("resumed history=%q", u.Text)
	}
	appServerTestKeys(t, u, "\r")
	appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"no"}}`)
	if len(u.InputHistory) != 1 || u.Text != "saved" {
		t.Fatalf("rejection added history or lost draft: %+v %q", u.InputHistory, u.Text)
	}
}

func TestAppServerInputHistoryUndoRestoresNavigation(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.rememberInput(conversationmodel.Input{Text: "first"})
	u.rememberInput(conversationmodel.Input{Text: "second"})
	appServerTestKeys(t, u, "draft\x1b[A\x1a\x1b[A")
	if u.Text != "second" {
		t.Fatalf("undo skipped newest: %q", u.Text)
	}
	appServerTestKeys(t, u, "\x1b[B2\x1b[A\x1a\x1a\x1a")
	// Undo recall, typing '2', and return-to-draft. The original browsing
	// session must retain its own draft, not the later edited one.
	if u.Text != "second" {
		t.Fatalf("undo browse=%q", u.Text)
	}
	appServerTestKeys(t, u, "\x1b[B")
	if u.Text != "draft" {
		t.Fatalf("older browse draft changed=%q", u.Text)
	}
}
