package launch

import (
	"github.com/yusing/mekugi/backends/codex/appserver"
	"os"
	"path/filepath"
	"strings"
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
	rememberInput(u.attached.conversation, conversationmodel.Input{Text: "old"})
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
	rememberInput(u.attached.conversation, conversationmodel.Input{Text: "old"})
	attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, path)
	appServerTestKeys(t, u, "\x1b[A")
	u.UndoDrafts = nil
	pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side, u.attached.nativeImages)
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

	u.startSession(u.Thread, "/workspace")
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
	rememberInput(u.attached.conversation, conversationmodel.Input{Text: "first"})
	rememberInput(u.attached.conversation, conversationmodel.Input{Text: "second"})
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

func TestAppServerInputHistoryFreshSessionRecallsExitDraft(t *testing.T) {
	directory := t.TempDir()
	first, _ := newAppServerTestUI()
	first.attached.inputHistory = newInputHistory([]string{"CODEX_HOME=" + directory}, directory)
	first.attached.inputHistory.selectThread(first.Thread)
	appServerTestRequest(t, first, "42", "config/read")
	appServerTestMessage(t, first, `{"id":42,"result":{"config":{"history":{"persistence":"save-all"}}}}`)
	// The acknowledged input goes through the real presentation effects path.
	appServerTestKeys(t, first, "submitted\r")
	appServerTestMessage(t, first, `{"id":1,"result":{}}`)
	first.AwaitingTurn = false
	appServerTestKeys(t, first, "foo")
	first.attached.inputHistory.saveDraft(first.attached.conversation, first.Editor)
	if err := first.attached.inputHistory.close(); err != nil {
		t.Fatal(err)
	}

	second, _ := newAppServerTestUI()
	second.attached.inputHistory = newInputHistory([]string{"CODEX_HOME=" + directory}, directory)
	second.attached.inputHistory.selectThread(second.Thread)
	appServerTestRequest(t, second, "42", "config/read")
	appServerTestMessage(t, second, `{"id":42,"result":{"config":{"history":{}}}}`)
	defer func() {
		if err := second.attached.inputHistory.close(); err != nil {
			t.Error(err)
		}
	}()
	result := <-second.attached.inputHistory.loads
	second.attached.inputHistory.loaded(second.attached.conversation, result)
	appServerTestKeys(t, second, "draft\x1b[D\x1b[A")
	if second.Text != "foo" {
		t.Fatalf("fresh session recall=%q", second.Text)
	}
	appServerTestKeys(t, second, "\x1b[A")
	if second.Text != "submitted" {
		t.Fatalf("acknowledged recall=%q", second.Text)
	}
	appServerTestKeys(t, second, "\x1b[B\x1b[B")
	if second.Text != "draft" || second.CursorBack != 1 {
		t.Fatalf("restored draft=%q caret=%d", second.Text, second.CursorBack)
	}
}

func TestAppServerInputHistoryDisabledAndReplyExclusion(t *testing.T) {
	for _, persistence := range []string{"none", "save-all"} {
		t.Run(persistence, func(t *testing.T) {
			directory := t.TempDir()
			u, _ := newAppServerTestUI()
			h := newInputHistory([]string{"CODEX_HOME=" + directory}, directory)
			h.selectThread(u.Thread)
			h.configure(appserver.Message{Result: []byte(`{"config":{"history":{"persistence":"` + persistence + `"}}}`)})
			// Pending question replies are never included in the saved exit draft.
			u.attached.conversation.Unsent = []conversationmodel.Input{{Text: "private reply", ReplyCount: 1}}
			u.Text = "ordinary"
			h.saveDraft(u.attached.conversation, u.Editor)
			if err := h.close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(directory, "history.jsonl"))
			if persistence == "none" {
				if !os.IsNotExist(err) {
					t.Fatalf("disabled history created: %v", err)
				}
			} else if err != nil || strings.Contains(string(data), "private reply") || !strings.Contains(string(data), "ordinary") {
				t.Fatalf("exit history=%q err=%v", data, err)
			}
		})
	}
}

func TestAppServerInputHistoryResumeReconcilesShared(t *testing.T) {
	for _, sharedFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume-first", true: "shared-first"}[sharedFirst], func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "history.jsonl"), []byte("{\"session_id\":\"main\",\"ts\":1,\"text\":\"saved\"}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			u, _ := newAppServerTestUI()
			h := newInputHistory([]string{"CODEX_HOME=" + directory}, directory)
			u.attached.inputHistory = h
			defer func() {
				if err := h.close(); err != nil {
					t.Error(err)
				}
			}()
			u.startSession(u.Thread, "/workspace")
			h.selectThread(u.Thread)
			h.configure(appserver.Message{Result: []byte(`{"config":{"history":{}}}`)})
			loaded := <-h.loads
			if sharedFirst {
				h.loaded(u.attached.conversation, loaded)
			}
			u.restoreHistory([]binding.HistoryTurn{{ID: "old", Status: "completed", Items: []binding.ThreadItem{{Type: "userMessage", ID: "input", Content: []byte(`[{"type":"text","text":"saved"}]`)}}}})
			if !sharedFirst {
				h.loaded(u.attached.conversation, loaded)
			}
			if len(u.InputHistory) != 1 {
				t.Fatalf("one persisted prompt recalled %d times: %+v", len(u.InputHistory), u.InputHistory)
			}
		})
	}
}

func TestAppServerHistoryResumePreservesLiveImageBinding(t *testing.T) {
	directory := t.TempDir()
	u, _ := newAppServerTestUI()
	h := newInputHistory([]string{"CODEX_HOME=" + directory}, directory)
	u.attached.inputHistory = h
	h.selectThread(u.Thread)
	h.configure(appserver.Message{Result: []byte(`{"config":{"history":{}}}`)})
	h.loaded(u.attached.conversation, <-h.loads)
	defer func() {
		if err := h.close(); err != nil {
			t.Error(err)
		}
	}()
	path := filepath.Join(directory, "image.png")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	attachImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, &u.ownedImages, u.side, path)
	appServerTestKeys(t, u, "\r")
	appServerTestMessage(t, u, `{"id":1,"result":{}}`)
	if len(u.InputHistory) != 1 || len(u.InputHistory[0].Images) != 1 {
		t.Fatalf("setup=%+v", u.InputHistory)
	}
	u.restoreHistory([]binding.HistoryTurn{{ID: "old", Status: "completed", Items: []binding.ThreadItem{{Type: "userMessage", ID: "input", Content: []byte(`[{"type":"text","text":"[Image 1]"}]`)}}}})
	if len(u.InputHistory) != 1 || len(u.InputHistory[0].Images) != 1 {
		t.Fatalf("resume dropped live image binding: %+v", u.InputHistory)
	}
}
func TestAppServerHistoryResumeLoadOrderPreservesNewest(t *testing.T) {
	for _, sharedFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume-first", true: "shared-first"}[sharedFirst], func(t *testing.T) {
			directory := t.TempDir()
			data := "{\"session_id\":\"main\",\"ts\":1,\"text\":\"saved\"}\n{\"session_id\":\"other\",\"ts\":2,\"text\":\"newest\"}\n"
			if err := os.WriteFile(filepath.Join(directory, "history.jsonl"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			u, _ := newAppServerTestUI()
			h := newInputHistory([]string{"CODEX_HOME=" + directory}, directory)
			u.attached.inputHistory = h
			defer func() {
				if err := h.close(); err != nil {
					t.Error(err)
				}
			}()
			u.startSession(u.Thread, "/workspace")
			h.selectThread(u.Thread)
			h.configure(appserver.Message{Result: []byte(`{"config":{"history":{}}}`)})
			loaded := <-h.loads
			if sharedFirst {
				h.loaded(u.attached.conversation, loaded)
			}
			u.restoreHistory([]binding.HistoryTurn{{ID: "old", Status: "completed", Items: []binding.ThreadItem{{Type: "userMessage", ID: "input", Content: []byte(`[{"type":"text","text":"saved"}]`)}}}})
			if !sharedFirst {
				h.loaded(u.attached.conversation, loaded)
			}
			appServerTestKeys(t, u, "\x1b[A")
			if u.Text != "newest" {
				t.Fatalf("newest history=%q entries=%+v", u.Text, u.InputHistory)
			}
		})
	}
}
