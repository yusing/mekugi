package router

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppServerInputHistoryAcknowledgedAndDraftRestored(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "first\r")
	appServerTestMessage(t, u, `{"id":1,"result":{}}`)
	u.starting = false
	appServerTestKeys(t, u, "second\r")
	appServerTestMessage(t, u, `{"id":2,"result":{}}`)
	u.starting = false
	appServerTestKeys(t, u, "draft\x1b[D\x1b[A")
	if u.draft != "second" {
		t.Fatalf("newest = %q", u.draft)
	}
	appServerTestKeys(t, u, "\x1b[A")
	if u.draft != "first" {
		t.Fatalf("oldest = %q", u.draft)
	}
	appServerTestKeys(t, u, "\x1b[A\x1b[B\x1b[B")
	if u.draft != "draft" || u.cursorBack != 1 {
		t.Fatalf("draft = %q cursorBack=%d", u.draft, u.cursorBack)
	}
}

func TestAppServerInputHistoryMultilineBoundaryAndUndo(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.rememberInput(composerDraft{text: "old"})
	appServerTestKeys(t, u, "one\ntwo\x1b[A")
	if u.draft != "one\ntwo" || u.cursor() != 3 {
		t.Fatalf("up moved into history before first row: %q at=%d", u.draft, u.cursor())
	}
	appServerTestKeys(t, u, "\x1b[A")
	if u.draft != "old" {
		t.Fatalf("history=%q", u.draft)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.draft != "one\ntwo" {
		t.Fatalf("undo recall=%q", u.draft)
	}
}

func TestAppServerInputHistoryAttachmentsRemainUsable(t *testing.T) {
	u, _ := newAppServerTestUI()
	path := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	u.rememberInput(composerDraft{text: "old"})
	u.attachImage(path)
	appServerTestKeys(t, u, "\x1b[A")
	u.undoDrafts = nil
	u.pruneDraftImages()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live draft image pruned: %v", err)
	}
	appServerTestKeys(t, u, "\x1b[B")
	if len(u.images) != 1 || u.images[0].path != path {
		t.Fatalf("restored images=%+v", u.images)
	}
	appServerTestKeys(t, u, "\r")
	appServerTestMessage(t, u, `{"id":1,"result":{}}`)
	appServerTestKeys(t, u, "\x1b[A")
	if len(u.images) != 1 || u.images[0].path != path {
		t.Fatalf("history images=%+v", u.images)
	}
}

func TestAppServerInputHistoryResumeAndRejectedInput(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.agents = newLiveActivityView()
	u.session.start(u.thread, "/workspace")
	u.restoreHistory([]appServerHistoryTurn{{ID: "old", Status: "completed", Items: []appServerItem{{Type: "userMessage", ID: "input", Content: []byte(`[{"type":"text","text":"saved"}]`)}}}})
	appServerTestKeys(t, u, "\x1b[A")
	if u.draft != "saved" {
		t.Fatalf("resumed history=%q", u.draft)
	}
	appServerTestKeys(t, u, "\r")
	appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"no"}}`)
	if len(u.inputHistory) != 1 || u.draft != "saved" {
		t.Fatalf("rejection added history or lost draft: %+v %q", u.inputHistory, u.draft)
	}
}

func TestAppServerInputHistoryUndoRestoresNavigation(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.rememberInput(composerDraft{text: "first"})
	u.rememberInput(composerDraft{text: "second"})
	appServerTestKeys(t, u, "draft\x1b[A\x1a\x1b[A")
	if u.draft != "second" {
		t.Fatalf("undo skipped newest: %q", u.draft)
	}
	appServerTestKeys(t, u, "\x1b[B2\x1b[A\x1a\x1a\x1a")
	// Undo recall, typing '2', and return-to-draft. The original browsing
	// session must retain its own draft, not the later edited one.
	if u.draft != "second" {
		t.Fatalf("undo browse=%q", u.draft)
	}
	appServerTestKeys(t, u, "\x1b[B")
	if u.draft != "draft" {
		t.Fatalf("older browse draft changed=%q", u.draft)
	}
}
