package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/conversation/composer"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestSideInputHistoryAcknowledgedAndPublished(t *testing.T) {
	for _, command := range []string{"/side", "/oneoff first"} {
		t.Run(command, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			appServerTestKeys(t, u, command+"\r")
			btwTestReply(t, u, btwTestRequest(t, wire, "thread/fork", "main"), `{"thread":{"id":"side"}}`)
			if command == "/side" {
				appServerTestKeys(t, u, "first\r")
			}
			start := btwTestRequest(t, wire, "turn/start", "side")
			if len(u.Editor.InputHistory) != 0 {
				t.Fatal("unacknowledged Side input entered history")
			}
			btwTestReply(t, u, start, `{"turn":{"id":"side-turn"}}`)
			appServerTestKeys(t, u, "second\r")
			btwTestReply(t, u, btwTestRequest(t, wire, "turn/steer", "side"), `{}`)
			appServerTestKeys(t, u, "draft\x1b[D\x1b[A")
			if u.Text != "second" {
				t.Fatalf("Side recall = %q", u.Text)
			}
			appServerTestKeys(t, u, "\x1b[A\x1b[B\x1b[B")
			if u.Text != "draft" || u.CursorBack != 1 {
				t.Fatal("Side recall lost the current draft or caret")
			}
			client := nativeTestPromptClient(u)
			client.editor("main").Remember(composer.Draft{Text: "main input"})
			for range 2 {
				client.event.Frame.Facts = nativePublicationFacts(&u.attached, nil, binding.TokenUsage{}, "", false, nil, "main", nil)
				client.acceptFacts("main")
			}
			side := client.editor("side:side")
			if len(side.InputHistory) != 2 || !side.Recall(true) || side.Text != "second" {
				t.Fatalf("native Side recall = %+v", side)
			}
			if len(client.editor("main").InputHistory) != 1 || len(u.attached.conversation.Editor.InputHistory) != 0 {
				t.Fatal("Side history mixed into Main")
			}
		})
	}
}

func TestSideInputHistoryRejectAndRetry(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/side\r")
	btwTestReply(t, u, btwTestRequest(t, wire, "thread/fork", "main"), `{"thread":{"id":"old-side"}}`)
	appServerTestKeys(t, u, "rejected\r")
	start := btwTestRequest(t, wire, "turn/start", "old-side")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"message":"rejected"}}`, start.ID))
	if u.Text != "rejected" || len(u.Editor.InputHistory) != 0 {
		t.Fatal("rejected Side input was remembered or lost")
	}
	appServerTestKeys(t, u, "\r")
	btwTestReply(t, u, btwTestRequest(t, wire, "turn/start", "old-side"), `{"turn":{"id":"old-turn"}}`)
	if len(u.Editor.InputHistory) != 1 || u.Editor.InputHistory[0].Text != "rejected" {
		t.Fatal("accepted retry did not enter Side history once")
	}
}

func TestSideInputHistoryRolloverKeepsAttachmentIdentity(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/side\r")
	btwTestReply(t, u, btwTestRequest(t, wire, "thread/fork", "main"), `{"thread":{"id":"side"}}`)
	client := nativeTestPromptClient(u)
	for i := range 101 {
		draft := composer.Draft{Text: "[Image 1]", Images: []composer.Token{{Start: 0, End: 9, Path: fmt.Sprintf("/image-%d.png", i)}}}
		u.side.AcknowledgeInput(conversationmodel.Input{Draft: draft})
		client.event.Frame.Facts = nativePublicationFacts(&u.attached, nil, binding.TokenUsage{}, "", false, nil, "main", nil)
		client.acceptFacts("main")
	}
	editor := client.editor("side:side")
	if len(editor.InputHistory) != 100 || !editor.Recall(true) || editor.Images[0].Path != "/image-100.png" {
		t.Fatal("bounded native history recalled another occurrence's image")
	}
}

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
	pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.side, u.attached.hooks.nativeImages)
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
