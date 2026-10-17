package router

import (
	"bytes"
	"encoding/json/jsontext"

	"github.com/yusing/mekugi/modules/activity"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	changeview "github.com/yusing/mekugi/modules/changehistory/terminalview"
	"github.com/yusing/mekugi/modules/usage"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	json "encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
)

type appServerTestInput struct {
	bytes.Buffer
	requestID string
	keys      terminalsession.Decoder
}

func (i *appServerTestInput) keyDecoder() *terminalsession.Decoder { return &i.keys }

// Synthetic host responses still enter through the real request ledger. The
// transport stub reserves their explicit IDs without adding fixture wire output.
type appServerTestTransport struct {
	client *appserver.Client
	input  *appServerTestInput
}

func (c appServerTestTransport) Send(method string, params any, request bool) (string, error) {
	if request && c.input.requestID != "" {
		id := c.input.requestID
		c.input.requestID = ""
		return id, nil
	}
	return c.client.Send(method, params, request)
}
func (c appServerTestTransport) Initialize() (string, error) {
	if c.input.requestID != "" {
		return c.Send("initialize", nil, true)
	}
	return c.client.Initialize()
}
func (c appServerTestTransport) Respond(id jsontext.Value, result any) error {
	return c.client.Respond(id, result)
}
func appServerTestRequest(t *testing.T, u *appServerUI, id, method string) {
	t.Helper()
	u.client.Input.(*appServerTestInput).requestID = id
	if err := u.request(method, nil); err != nil {
		t.Fatal(err)
	}
}
func (*appServerTestInput) Close() error { return nil }

func newAppServerTestUI() (*appServerUI, *appServerTestInput) {
	w := new(appServerTestInput)
	client := &appserver.Client{Input: w}
	u := appServerTestFeatures(&appServerUI{session: appServerSession{activity: new(activitymodel.Service)}, issues: testCriticalErrors(), history: testHistory(), client: client, view: activityui.NewView(), requests: sessioncontrol.NewRequests[appServerRequest](appServerTestTransport{client, w}), Thread: "main"})
	u.approvals.grants = sessioncontrol.NewApprovalGrants(testGuards())
	u.view.Painter.FileLink = u.markdownFileAllowed
	return u, w
}

func TestAppServerUIDelayedSubmissionEditing(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		u, _ := newAppServerTestUI()
		appServerTestKeys(t, u, "first\rnext")
		if u.Text != "next" {
			t.Fatalf("accepted input leaked into draft: %q", u.Text)
		}
		if rejected {
			appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"rejected"}}`)
			if u.Text != "first\nnext" {
				t.Fatalf("lost rejected input or new typing: %q", u.Text)
			}
		} else {
			appServerTestMessage(t, u, `{"id":1,"result":{"turn":{"id":"t"}}}`)
			if u.Text != "next" {
				t.Fatalf("lost new typing: %q", u.Text)
			}
		}
	}
}

func TestAppServerUIEscapeAndLongDraft(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "\x1bhello")
	if u.Text != "hello" {
		t.Fatalf("Escape swallowed input: %q", u.Text)
	}
	u.Text = strings.Repeat("a", 88) + "VISIBLE_TAIL"
	var frame bytes.Buffer
	if err := u.testPaint(&frame, 24, 12); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(appServerComposerScreenText(t, frame.Bytes(), 24, 12), "VISIBLE_TAIL") {
		t.Fatalf("insertion point not visible: %q", frame.String())
	}
	u.Text = ""
	appServerTestKeys(t, u, "\x1b")
	if !w.keys.Pending() || u.Text != "" || w.Len() != 0 {
		t.Fatal("partial paste start was interpreted before its continuation")
	}
	appServerTestKeys(t, u, "[200~split\rpaste\x1b")
	if !w.keys.Pasting() || w.Len() != 0 {
		t.Fatal("partial paste terminator ended paste or submitted input")
	}
	appServerTestKeys(t, u, "[201~")
	if u.Text != "split\npaste" || w.keys.Pasting() || w.keys.Pending() || w.Len() != 0 {
		t.Fatal("split paste submitted or lost data")
	}
}

func TestAppServerCtrlCClearsDraftThenInterruptsOrQuits(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "draft\x03")
	if u.Text != "" || w.Len() != 0 || !strings.Contains(u.feedback.Text, "Ctrl-C again quits") {
		t.Fatalf("first Ctrl-C draft=%q notice=%q", u.Text, u.feedback.Text)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.Text != "draft" || u.feedback.Text != "" {
		t.Fatalf("cleared draft not restorable: %q notice=%q", u.Text, u.feedback.Text)
	}
	u.Text = ""
	if quit, err := u.key("\x03"); !quit || err != nil {
		t.Fatalf("idle Ctrl-C quit=%v err=%v", quit, err)
	}
	u, w = newAppServerTestUI()
	u.Turn = "turn"
	appServerTestKeys(t, u, "steer\x03")
	if u.Text != "" || w.Len() != 0 || !strings.Contains(u.feedback.Text, "Ctrl-C again interrupts") {
		t.Fatalf("draft not cleared before interrupt: %q sent=%q", u.Text, w.String())
	}
	if quit, err := u.key("\x03"); quit || err != nil || !strings.Contains(w.String(), "turn/interrupt") {
		t.Fatalf("active Ctrl-C quit=%v err=%v sent=%q", quit, err, w.String())
	}
	u, w = newAppServerTestUI()
	u.AwaitingTurn = true
	if quit, err := u.key("\x03"); quit || err != nil || w.Len() != 0 {
		t.Fatalf("starting Ctrl-C quit=%v err=%v", quit, err)
	}
}

func TestAppServerComposerNoticeKeepsTurnStateAndClearsOnEdit(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Turn, u.sessionPresentation().Status.Text = "turn", "Working"
	appServerTestKeys(t, u, "/nope\r")
	label := ansi.Strip(u.stateLabel(time.Now()))
	if !strings.Contains(label, "Working") || !strings.Contains(label, "Unknown command /nope") {
		t.Fatalf("notice replaced turn state: %q", label)
	}
	appServerTestKeys(t, u, "\x7f")
	if u.feedback.Text != "" || u.sessionPresentation().Status.Text != "Working" {
		t.Fatalf("edit kept stale notice %q or lost status %q", u.feedback.Text, u.sessionPresentation().Status.Text)
	}
}

func TestAppServerIdleStateUsesDefaultText(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.sessionPresentation().Status.Text = "Ready"
	if label := u.stateLabel(time.Now()); label != "\x1b[39mReady"+terminalui.Reset {
		t.Fatalf("idle label = %q", label)
	}
}

func appServerTestKeys(t *testing.T, u *appServerUI, keys string) {
	t.Helper()
	decoder := u.client.Input.(interface {
		keyDecoder() *terminalsession.Decoder
	}).keyDecoder()
	for _, key := range []byte(keys) {
		if quit, err := u.input(decoder.Feed(key, u.now())); quit || err != nil {
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
	if u.Text != "你好" {
		t.Fatalf("Unicode backspace: %q", u.Text)
	}
	u.Text = ""
	appServerTestKeys(t, u, "\x7f\x1b[200~one\rtwo\n你好\x1b[201~")
	if u.Text != "one\ntwo\n你好" || w.keys.Pasting() || w.Len() != 0 {
		t.Fatalf("paste submitted or corrupted: draft=%q paste=%v wire=%q", u.Text, w.keys.Pasting(), w.String())
	}
	u.Text = "/unsupported"
	appServerTestKeys(t, u, "\r")
	if w.Len() != 0 || u.Text != "/unsupported" {
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
	if u.Text != "你好" {
		t.Fatalf("shell corrupted typed Unicode: %q", u.Text)
	}
}

func TestAppServerUIImmediateInputEcho(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		u, _ := newAppServerTestUI()
		u.view.Following = false
		appServerTestKeys(t, u, "你好 first message\r")
		var frame bytes.Buffer
		if err := u.testPaint(&frame, 100, 24); err != nil {
			t.Fatal(err)
		}
		if !u.view.Following || !strings.Contains(frame.String(), "你好 first message") {
			t.Fatal("submitted input disappeared while waiting for app-server")
		}
		if rejected {
			appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"rejected"}}`)
			if len(u.source.Entries) != 0 || u.Text != "你好 first message" || u.Starting() {
				t.Fatal("rejection retained a false user message or lost the draft")
			}
			continue
		}
		for _, method := range []string{"item/started", "item/completed"} {
			appServerTestMessage(t, u, `{"method":"`+method+`","params":{"threadId":"main","turnId":"t","item":{"id":"user-1","type":"userMessage","content":[{"type":"text","text":"你好 first message"}]}}}`)
		}
		appServerTestMessage(t, u, `{"id":1,"result":{"turn":{"id":"t"}}}`)
		if len(u.source.Entries) != 1 || u.source.Entries[0].Source.Native.Item != "user-1" {
			t.Fatal("server echo duplicated the local user message")
		}
	}
}

func TestAppServerUIStartAndRejectedSteer(t *testing.T) {
	u, w := newAppServerTestUI()
	u.Text = "first"
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
	if !u.Starting() || u.Text != "" {
		t.Fatal("response must clear accepted draft but retain start guard until notification")
	}
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"turn-1"}}}`)
	w.Reset()
	u.Text = "steer me"
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
	if u.Text != "steer me" || u.Turn != "turn-1" || w.Len() != 0 {
		t.Fatal("rejected steer lost draft or silently started a new turn")
	}
}

func TestAppServerUINotificationBeforeResponseAndItems(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Text = "first"
	appServerTestKeys(t, u, "\r")
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"t"}}}`)
	u.Text = "next draft"
	appServerTestMessage(t, u, `{"id":1,"result":{"turn":{"id":"t"}}}`)
	if u.Turn != "t" || u.Text != "next draft" {
		t.Fatal("late response reset active identity or newer draft")
	}
	for _, wire := range []string{
		`{"method":"item/started","params":{"threadId":"main","turnId":"t","item":{"id":"i","type":"agentMessage","text":""}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"main","turnId":"t","itemId":"i","delta":"hel"}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"main","turnId":"t","itemId":"i","delta":"lo"}}`,
	} {
		appServerTestMessage(t, u, wire)
	}
	if len(u.source.Entries) != 2 || u.source.Entries[1].Source.Text != "hello" {
		t.Fatalf("delta assembly: %+v", u.source.Entries)
	}
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"t","item":{"id":"i","type":"agentMessage","text":"hello!"}}}`)
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"child","turnId":"t","item":{"id":"i","type":"agentMessage","text":"child answer"}}}`)
	if len(u.source.Entries) != 3 || u.source.Entries[1].Source.Text != "hello!" || u.source.Entries[2].Source.Agent != "Thread child" {
		t.Fatalf("completion or cross-thread collision: %+v", u.source.Entries)
	}
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"t","status":"completed"}}}`)
	if u.Turn != "t" {
		t.Fatal("child completion cleared main turn")
	}
	u.agents = activityui.NewView() // Public-reasoning projection has both feed consumers.
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"t","item":{"id":"r","type":"reasoning","text":"PRIVATE_REASONING"}}}`)
	appServerTestMessage(t, u, `{"method":"item/reasoning/textDelta","params":{"threadId":"main","turnId":"t","itemId":"r","delta":"PRIVATE_REASONING"}}`)
	var frame bytes.Buffer
	if err := u.testPaint(&frame, 100, 30); err != nil {
		t.Fatal(err)
	}
	if len(u.source.Entries) != 3 || strings.Contains(frame.String(), "PRIVATE_REASONING") || !strings.Contains(frame.String(), "child answer") {
		t.Fatalf("unexpected visible content: %q", frame.String())
	}
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"t","status":"completed"}}}`)
	if u.Turn != "" || !strings.Contains(u.sessionLabel(u.now()), "Completed in ") {
		t.Fatal("main completion not applied")
	}
}

func TestAppServerSteerStatusAcknowledgement(t *testing.T) {
	for _, completed := range []bool{false, true} {
		u, _ := newAppServerTestUI()
		appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"t"}}}`)
		appServerTestKeys(t, u, "steer\r")
		if u.sessionPresentation().Status.Text != "Sending…" {
			t.Fatal(u.sessionPresentation().Status.Text)
		}
		if completed {
			appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"t","status":"completed"}}}`)
		}
		before := u.sessionPresentation().Status.Text
		appServerTestMessage(t, u, `{"id":1,"result":{"turnId":"t"}}`)
		if completed {
			if u.sessionPresentation().Status.Text != before || u.Turn != "" {
				t.Fatal("late acknowledgement revived completed turn")
			}
		} else if u.sessionPresentation().Status.Text != "Working" || u.Turn != "t" || u.conversation().Submitted[u.Submission().ID].Text != "" {
			t.Fatalf("steer stayed pending: status=%s turn=%s submitted=%s", u.sessionPresentation().Status.Text, u.Turn, u.conversation().Submitted[u.Submission().ID].Text)
		}
	}
}

func TestAppServerNoticeExpiry(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	u.sessionPresentation().Status.Text = "Working"
	u.shell.copyText("copied")
	deadline := u.feedback.Until
	if deadline.IsZero() || u.expireNotice(deadline.Add(-time.Millisecond)) {
		t.Fatal("notice expired early")
	}
	u.shell.copyText("copied again")
	if u.feedback.Until.Before(deadline) {
		t.Fatal("repeat notice did not refresh deadline")
	}
	if !u.expireNotice(u.feedback.Until) || u.feedback.Text != "" || u.sessionPresentation().Status.Text != "Working" {
		t.Fatal("notice did not expire independently of status")
	}
	u.setNotice("failure", true)
	if u.expireNotice(time.Now().Add(time.Hour)) || u.feedback.Text != "failure" {
		t.Fatal("error expired")
	}
	u.insertDraft("x")
	if u.feedback.Text != "" || !u.feedback.Until.IsZero() {
		t.Fatal("editing did not clear notice")
	}
}

func TestNativeUIEscapeInterruptPreservesDraft(t *testing.T) {
	for _, active := range []bool{false, true} {
		u, w := newAppServerTestUI()
		u.ensureShell()
		u.Text = "unfinished draft"
		if active {
			u.Turn = "turn"
		}
		if err := u.shell.key(27); err != nil {
			t.Fatal(err)
		}
		ageTerminalEscape(u.shell)
		if err := u.shell.flushEscape(); err != nil {
			t.Fatal(err)
		}
		if u.Text != "unfinished draft" || u.quitRequested {
			t.Fatalf("Escape changed draft or quit: %+v", u.Text)
		}
		if active {
			appServerOneRequest(t, w, "turn/interrupt", "")
			if u.Interruption.Target != "turn" {
				t.Fatal("turn not interrupting")
			}
		} else if w.Len() != 0 {
			t.Fatalf("idle Escape sent %q", w.String())
		}
		if err := u.shell.key('x'); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(u.Text, "x") {
			t.Fatal("Escape swallowed subsequent input")
		}
	}
}

// appServerTestFeatures supplies the same core owner to the composer and transcript.
func appServerTestFeatures(u *appServerUI) *appServerUI {
	u.reviewSource = new(changecapture.Service)
	u.terminalViews = testTerminalViews(u.reviewSource)
	u.presentationState = testPresentation()
	u.conversationState = new(conversationmodel.Service)
	observations := conversationbinding.Codex.Observation(u.conversationState)
	u.session.observations = &observations
	u.session.transcript = observations.NewTranscript(u.session.Scope())
	u.attachPresentation()
	u.settlePresentation()
	return u
}

func testPresentation() *sessionpresentation.Service {
	service, err := sessionpresentation.New(usage.Module.Contributes.Presentation)
	if err != nil {
		panic(err)
	}
	return service
}

func testTerminalViews(source *changecapture.Service) terminalsession.Views {
	definition := changeview.Views.Auxiliary[0]
	views, err := terminalsession.NewViews([]terminalui.PaneFactory[*scope.Store]{{Name: definition.Name, Label: definition.Label, Attach: func(theme diffrender.Theme) terminalui.PaneAttachment[*scope.Store] {
		return definition.Attach(source, theme)
	}}}, []terminalui.RosterDefinition[*activityui.View]{activity.Module.Views})
	if err != nil {
		panic(err)
	}
	return views
}

func savedPane(u *terminalUI) *changeview.Pane { return u.Auxiliary.(*changeview.Pane) }

func newActivityAdapter() *activityAdapter { return newTranscriptAdapter(activityui.NewView(), 0) }
