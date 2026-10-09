package router

import (
	"bytes"
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/appserver"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

type appServerTestInput struct{ bytes.Buffer }

func (*appServerTestInput) Close() error { return nil }

func newAppServerTestUI() (*appServerUI, *appServerTestInput) {
	w := new(appServerTestInput)
	u := &appServerUI{client: &appserver.Client{Input: w}, view: newLiveActivityView(), requests: make(map[string]string), thread: "main"}
	u.view.painter.FileLink = u.markdownFileExists
	return u, w
}

func TestAppServerUIDelayedSubmissionEditing(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		u, _ := newAppServerTestUI()
		appServerTestKeys(t, u, "first\rnext")
		if u.draft != "next" {
			t.Fatalf("accepted input leaked into draft: %q", u.draft)
		}
		if rejected {
			appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"rejected"}}`)
			if u.draft != "first\nnext" {
				t.Fatalf("lost rejected input or new typing: %q", u.draft)
			}
		} else {
			appServerTestMessage(t, u, `{"id":1,"result":{"turn":{"id":"t"}}}`)
			if u.draft != "next" {
				t.Fatalf("lost new typing: %q", u.draft)
			}
		}
	}
}

func TestAppServerUIEscapeAndLongDraft(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "\x1bhello")
	if u.draft != "hello" {
		t.Fatalf("Escape swallowed input: %q", u.draft)
	}
	u.draft = strings.Repeat("a", 88) + "VISIBLE_TAIL"
	var frame bytes.Buffer
	if err := u.paint(&frame, 24, 12); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(appServerComposerScreenText(t, frame.Bytes(), 24, 12), "VISIBLE_TAIL") {
		t.Fatalf("insertion point not visible: %q", frame.String())
	}
	u.draft = ""
	appServerTestKeys(t, u, "\x1b")
	appServerTestKeys(t, u, "[200~split\rpaste\x1b")
	appServerTestKeys(t, u, "[201~")
	if u.draft != "split\npaste" || w.Len() != 0 {
		t.Fatal("split paste submitted or lost data")
	}
}

func TestAppServerCtrlCClearsDraftThenInterruptsOrQuits(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "draft\x03")
	if u.draft != "" || w.Len() != 0 || !strings.Contains(u.notice, "Ctrl-C again quits") {
		t.Fatalf("first Ctrl-C draft=%q notice=%q", u.draft, u.notice)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.draft != "draft" || u.notice != "" {
		t.Fatalf("cleared draft not restorable: %q notice=%q", u.draft, u.notice)
	}
	u.draft = ""
	if quit, err := u.key(3); !quit || err != nil {
		t.Fatalf("idle Ctrl-C quit=%v err=%v", quit, err)
	}
	u, w = newAppServerTestUI()
	u.turn = "turn"
	appServerTestKeys(t, u, "steer\x03")
	if u.draft != "" || w.Len() != 0 || !strings.Contains(u.notice, "Ctrl-C again interrupts") {
		t.Fatalf("draft not cleared before interrupt: %q sent=%q", u.draft, w.String())
	}
	if quit, err := u.key(3); quit || err != nil || !strings.Contains(w.String(), "turn/interrupt") {
		t.Fatalf("active Ctrl-C quit=%v err=%v sent=%q", quit, err, w.String())
	}
	u, w = newAppServerTestUI()
	u.awaitingTurn = true
	if quit, err := u.key(3); quit || err != nil || w.Len() != 0 {
		t.Fatalf("starting Ctrl-C quit=%v err=%v", quit, err)
	}
}

func TestAppServerComposerNoticeKeepsTurnStateAndClearsOnEdit(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.turn, u.status = "turn", "Working"
	appServerTestKeys(t, u, "/nope\r")
	label := ansi.Strip(u.stateLabel(time.Now()))
	if !strings.Contains(label, "Working") || !strings.Contains(label, "Unknown command /nope") {
		t.Fatalf("notice replaced turn state: %q", label)
	}
	appServerTestKeys(t, u, "\x7f")
	if u.notice != "" || u.status != "Working" {
		t.Fatalf("edit kept stale notice %q or lost status %q", u.notice, u.status)
	}
}

func TestAppServerIdleStateUsesDefaultText(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.status = "Ready"
	if label := u.stateLabel(time.Now()); label != "\x1b[39mReady"+activityui.Reset {
		t.Fatalf("idle label = %q", label)
	}
}

func appServerTestKeys(t *testing.T, u *appServerUI, keys string) {
	t.Helper()
	for _, key := range []byte(keys) {
		if quit, err := u.key(key); quit || err != nil {
			t.Fatalf("key %q: quit=%v err=%v", key, quit, err)
		}
	}
}

func appServerTestMessage(t *testing.T, u *appServerUI, wire string) {
	t.Helper()
	var m appserver.Message
	if err := json.Unmarshal([]byte(wire), &m); err != nil {
		t.Fatal(err)
	}
	if err := u.message(m); err != nil {
		t.Fatal(err)
	}
}

func TestAppServerUIInput(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "你好🙂\x7f")
	if u.draft != "你好" {
		t.Fatalf("Unicode backspace: %q", u.draft)
	}
	u.draft = ""
	appServerTestKeys(t, u, "\x7f\x1b[200~one\rtwo\n你好\x1b[201~")
	if u.draft != "one\ntwo\n你好" || u.paste || w.Len() != 0 {
		t.Fatalf("paste submitted or corrupted: draft=%q paste=%v wire=%q", u.draft, u.paste, w.String())
	}
	u.draft = "/unsupported"
	appServerTestKeys(t, u, "\r")
	if w.Len() != 0 || u.draft != "/unsupported" {
		t.Fatalf("unsupported slash submitted: %q", w.String())
	}
}

func TestAppServerUIShellUnicodeInput(t *testing.T) {
	u, _ := newAppServerTestUI()
	shell := &terminalUI{main: u}
	for _, key := range []byte("你好🙂\x7f") {
		if err := shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.draft != "你好" {
		t.Fatalf("shell corrupted typed Unicode: %q", u.draft)
	}
}

func TestAppServerUIImmediateInputEcho(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		u, _ := newAppServerTestUI()
		u.view.following = false
		appServerTestKeys(t, u, "你好 first message\r")
		var frame bytes.Buffer
		if err := u.paint(&frame, 100, 24); err != nil {
			t.Fatal(err)
		}
		if !u.view.following || !strings.Contains(frame.String(), "你好 first message") {
			t.Fatal("submitted input disappeared while waiting for app-server")
		}
		if rejected {
			appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"rejected"}}`)
			if len(u.view.entries) != 0 || u.draft != "你好 first message" || u.starting() {
				t.Fatal("rejection retained a false user message or lost the draft")
			}
			continue
		}
		for _, method := range []string{"item/started", "item/completed"} {
			appServerTestMessage(t, u, `{"method":"`+method+`","params":{"threadId":"main","turnId":"t","item":{"id":"user-1","type":"userMessage","content":[{"type":"text","text":"你好 first message"}]}}}`)
		}
		appServerTestMessage(t, u, `{"id":1,"result":{"turn":{"id":"t"}}}`)
		if len(u.view.entries) != 1 || u.view.entries[0].native.item != "user-1" {
			t.Fatal("server echo duplicated the local user message")
		}
	}
}

func TestAppServerUIStartAndRejectedSteer(t *testing.T) {
	u, w := newAppServerTestUI()
	u.draft = "first"
	appServerTestKeys(t, u, "\r\r")
	var start struct {
		Method string `json:"method"`
		Params struct {
			ThreadID       string `json:"threadId"`
			ExpectedTurnID string `json:"expectedTurnId"`
			Input          []struct {
				Text string `json:"text"`
			} `json:"input"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(w.Bytes()), &start); err != nil {
		t.Fatal(err)
	}
	if start.Method != "turn/start" || start.Params.ThreadID != "main" || start.Params.ExpectedTurnID != "" || len(start.Params.Input) != 1 || start.Params.Input[0].Text != "first" {
		t.Fatalf("wrong start: %+v", start)
	}
	appServerTestMessage(t, u, `{"id":1,"result":{"turn":{"id":"turn-1"}}}`)
	if !u.starting() || u.draft != "" {
		t.Fatal("response must clear accepted draft but retain start guard until notification")
	}
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"turn-1"}}}`)
	w.Reset()
	u.draft = "steer me"
	appServerTestKeys(t, u, "\r")
	var steer struct {
		Method string `json:"method"`
		Params struct {
			ThreadID       string `json:"threadId"`
			ExpectedTurnID string `json:"expectedTurnId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(w.Bytes()), &steer); err != nil {
		t.Fatal(err)
	}
	if steer.Method != "turn/steer" || steer.Params.ThreadID != "main" || steer.Params.ExpectedTurnID != "turn-1" {
		t.Fatalf("wrong steer identity: %+v", steer)
	}
	w.Reset()
	appServerTestMessage(t, u, `{"id":2,"error":{"code":-1,"message":"turn changed"}}`)
	if u.draft != "steer me" || u.turn != "turn-1" || w.Len() != 0 {
		t.Fatal("rejected steer lost draft or silently started a new turn")
	}
}

func TestAppServerUINotificationBeforeResponseAndItems(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.draft = "first"
	appServerTestKeys(t, u, "\r")
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"t"}}}`)
	u.draft = "next draft"
	appServerTestMessage(t, u, `{"id":1,"result":{"turn":{"id":"t"}}}`)
	if u.turn != "t" || u.draft != "next draft" {
		t.Fatal("late response reset active identity or newer draft")
	}
	for _, wire := range []string{
		`{"method":"item/started","params":{"threadId":"main","turnId":"t","item":{"id":"i","type":"agentMessage","text":""}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"main","turnId":"t","itemId":"i","delta":"hel"}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"main","turnId":"t","itemId":"i","delta":"lo"}}`,
	} {
		appServerTestMessage(t, u, wire)
	}
	if len(u.view.entries) != 2 || u.view.entries[1].Text != "hello" {
		t.Fatalf("delta assembly: %+v", u.view.entries)
	}
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"t","item":{"id":"i","type":"agentMessage","text":"hello!"}}}`)
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"child","turnId":"t","item":{"id":"i","type":"agentMessage","text":"child answer"}}}`)
	if len(u.view.entries) != 3 || u.view.entries[1].Text != "hello!" || u.view.entries[2].Agent != "Thread child" {
		t.Fatalf("completion or cross-thread collision: %+v", u.view.entries)
	}
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"t","status":"completed"}}}`)
	if u.turn != "t" {
		t.Fatal("child completion cleared main turn")
	}
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"t","item":{"id":"r","type":"reasoning","text":"PRIVATE_REASONING"}}}`)
	appServerTestMessage(t, u, `{"method":"item/reasoning/textDelta","params":{"threadId":"main","turnId":"t","itemId":"r","delta":"PRIVATE_REASONING"}}`)
	var frame bytes.Buffer
	if err := u.paint(&frame, 100, 30); err != nil {
		t.Fatal(err)
	}
	if len(u.view.entries) != 3 || strings.Contains(frame.String(), "PRIVATE_REASONING") || !strings.Contains(frame.String(), "child answer") {
		t.Fatalf("unexpected visible content: %q", frame.String())
	}
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"t","status":"completed"}}}`)
	if u.turn != "" || !strings.HasPrefix(u.status, "Completed in ") {
		t.Fatal("main completion not applied")
	}
}

func TestAppServerSteerStatusAcknowledgement(t *testing.T) {
	for _, completed := range []bool{false, true} {
		u, _ := newAppServerTestUI()
		appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"t"}}}`)
		appServerTestKeys(t, u, "steer\r")
		if u.status != "Sending…" {
			t.Fatal(u.status)
		}
		if completed {
			appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"t","status":"completed"}}}`)
		}
		before := u.status
		appServerTestMessage(t, u, `{"id":1,"result":{"turnId":"t"}}`)
		if completed {
			if u.status != before || u.turn != "" {
				t.Fatal("late acknowledgement revived completed turn")
			}
		} else if u.status != "Working" || u.turn != "t" || u.submission.text != "" {
			t.Fatalf("steer stayed pending: status=%s turn=%s submitted=%s", u.status, u.turn, u.submission.text)
		}
	}
}

func TestAppServerNoticeExpiry(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	u.status = "Working"
	u.shell.copyText("copied")
	deadline := u.noticeUntil
	if deadline.IsZero() || u.expireNotice(deadline.Add(-time.Millisecond)) {
		t.Fatal("notice expired early")
	}
	u.shell.copyText("copied again")
	if u.noticeUntil.Before(deadline) {
		t.Fatal("repeat notice did not refresh deadline")
	}
	if !u.expireNotice(u.noticeUntil) || u.notice != "" || u.status != "Working" {
		t.Fatal("notice did not expire independently of status")
	}
	u.setNotice("failure", true)
	if u.expireNotice(time.Now().Add(time.Hour)) || u.notice != "failure" {
		t.Fatal("error expired")
	}
	u.insertDraft("x")
	if u.notice != "" || !u.noticeUntil.IsZero() {
		t.Fatal("editing did not clear notice")
	}
}

func TestNativeUIEscapeInterruptPreservesDraft(t *testing.T) {
	for _, active := range []bool{false, true} {
		u, w := newAppServerTestUI()
		u.ensureShell()
		u.draft = "unfinished draft"
		if active {
			u.turn = "turn"
		}
		if err := u.shell.key(27); err != nil {
			t.Fatal(err)
		}
		u.shell.sequenceAt = time.Now().Add(-time.Second)
		if err := u.shell.flushEscape(); err != nil {
			t.Fatal(err)
		}
		if u.draft != "unfinished draft" || u.quitRequested {
			t.Fatalf("Escape changed draft or quit: %+v", u.draft)
		}
		if active {
			appServerOneRequest(t, w, "turn/interrupt", "")
			if u.interruption.target != "turn" {
				t.Fatal("turn not interrupting")
			}
		} else if w.Len() != 0 {
			t.Fatalf("idle Escape sent %q", w.String())
		}
		if err := u.shell.key('x'); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(u.draft, "x") {
			t.Fatal("Escape swallowed subsequent input")
		}
	}
}
