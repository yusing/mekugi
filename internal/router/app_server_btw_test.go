package router

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

type btwTestRPC struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params struct {
		ThreadID     string         `json:"threadId"`
		TurnID       string         `json:"turnId"`
		Ephemeral    bool           `json:"ephemeral"`
		ExcludeTurns bool           `json:"excludeTurns"`
		Config       map[string]any `json:"config"`
		Input        []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"input"`
	} `json:"params"`
}

func btwTestRequest(t *testing.T, w *appServerTestInput, method, thread string) btwTestRPC {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(w.Bytes()), []byte{'\n'})
	var r btwTestRPC
	if len(lines) != 1 {
		t.Fatalf("want one %s request, got %s", method, w.String())
	}
	if err := json.Unmarshal(lines[0], &r); err != nil {
		t.Fatal(err)
	}
	w.Reset()
	if r.Method != method || r.Params.ThreadID != thread {
		t.Fatalf("request = %+v, want %s on %s", r, method, thread)
	}
	return r
}

func btwTestReply(t *testing.T, u *appServerUI, r btwTestRPC, result string) {
	t.Helper()
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":%s}`, r.ID, result))
}

func btwTestStart(t *testing.T, u *appServerUI, w *appServerTestInput) {
	t.Helper()
	appServerTestKeys(t, u, "/btw why?\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	if !fork.Params.Ephemeral || !fork.Params.ExcludeTurns {
		t.Fatalf("fork is not ephemeral snapshot: %+v", fork.Params)
	}
	btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
	start := btwTestRequest(t, w, "turn/start", "side")
	var question string
	for _, input := range start.Params.Input {
		question += input.Text
	}
	if !strings.Contains(question, "why?") || strings.Contains(question, "/btw why?") {
		t.Fatalf("side input = %q", question)
	}
	btwTestReply(t, u, start, `{"turn":{"id":"side-turn"}}`)
}

func TestAppServerBTWDisablesOnlyForkPrewarm(t *testing.T) {
	for _, effort := range []string{"", "high"} {
		t.Run(effort, func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.statusConfig.Provider, u.reasoningEffort = "mekugi_wrap", effort
			appServerTestKeys(t, u, "/btw question\r")
			r := btwTestRequest(t, w, "thread/fork", "main")
			if value, ok := r.Params.Config["model_providers.mekugi_wrap.supports_websockets"]; !ok || value != false {
				t.Fatalf("fork did not disable provider prewarm: %+v", r.Params.Config)
			}
			if effort != "" && r.Params.Config["model_reasoning_effort"] != effort {
				t.Fatal("prewarm override replaced reasoning override")
			}
			appServerTestKeys(t, u, "Main stays independent\r")
			main := btwTestRequest(t, w, "turn/start", "main")
			if len(main.Params.Config) != 0 || u.statusConfig.Provider != "mekugi_wrap" || u.reasoningEffort != effort {
				t.Fatal("side override mutated Main settings")
			}
		})
	}
}

func TestAppServerBTWIsolationAndFollowUp(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.session.start("main", t.TempDir())
			u.status = "main status"
			if active {
				u.turn = "main-turn"
			}
			turn, status, entries, agents, usage := u.turn, u.status, len(u.view.entries), len(u.session.agents), u.exitUsage
			btwTestStart(t, u, w)
			if u.draft != "" {
				t.Fatalf("side draft retained: %q", u.draft)
			}
			appServerTestMessage(t, u, `{"method":"thread/started","params":{"thread":{"id":"side"}}}`)
			appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"side","turn":{"id":"side-turn"}}}`)
			appServerTestMessage(t, u, `{"method":"item/agentMessage/delta","params":{"threadId":"side","turnId":"side-turn","itemId":"answer","delta":"**Side answer**"}}`)
			if len(u.btw.answer) != 1 || u.btw.answer[0].text != "**Side answer**" {
				t.Fatalf("stream missing: %+v", u.btw.answer)
			}
			appServerTestMessage(t, u, `{"method":"thread/tokenUsage/updated","params":{"threadId":"side","tokenUsage":{"total":{"inputTokens":9999,"outputTokens":123}}}}`)
			appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"side","turn":{"id":"side-turn","status":"completed"}}}`)
			if u.thread != "main" || u.turn != turn || u.status != status || len(u.view.entries) != entries || len(u.session.agents) != agents || u.exitUsage != usage {
				t.Fatal("side events mutated main conversation")
			}
			appServerTestKeys(t, u, "/btw explain more\r")
			follow := btwTestRequest(t, w, "turn/start", "side")
			btwTestReply(t, u, follow, `{"turn":{"id":"follow"}}`)
			appServerTestKeys(t, u, "main prompt\r")
			method := "turn/start"
			if active {
				method = "turn/steer"
			}
			btwTestRequest(t, w, method, "main")
		})
	}
}

func TestAppServerBTWBusyRetainsDraft(t *testing.T) {
	u, w := newAppServerTestUI()
	btwTestStart(t, u, w)
	appServerTestKeys(t, u, "/btw next question\r")
	if u.draft != "/btw next question" || w.Len() != 0 || u.btw.question != "why?" {
		t.Fatalf("busy submission lost draft or sent: draft=%q wire=%s", u.draft, w.String())
	}
}

func TestAppServerBTWWaitsForMainStartAcknowledgement(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "main prompt\r")
	main := btwTestRequest(t, w, "turn/start", "main")
	appServerTestKeys(t, u, "/btw why?\r")
	if w.Len() != 0 {
		t.Fatalf("fork raced unresolved main submission: %s", w.String())
	}
	btwTestReply(t, u, main, `{"turn":{"id":"main-turn"}}`)
	if w.Len() != 0 {
		t.Fatalf("fork raced main turn/started: %s", w.String())
	}
	appServerTestTurn(t, u, "main-turn")
	btwTestRequest(t, w, "thread/fork", "main")
}

func TestAppServerBTWTurnFailureDetailsAreLocal(t *testing.T) {
	u, w := newAppServerTestUI()
	u.turn, u.status = "main-turn", "Working"
	btwTestStart(t, u, w)
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"side","turn":{"id":"side-turn","status":"failed","error":{"message":"side model failed"}}}}`)
	if u.btw.busy || !u.btw.alert || u.btw.status != "side model failed" || u.turn != "main-turn" || u.status != "Working" {
		t.Fatalf("side failure changed Main or lost detail: %+v", u.btw)
	}
}

func TestAppServerBTWUnsolicitedSideEventsDoNotReachMain(t *testing.T) {
	u, w := newAppServerTestUI()
	u.session.start("main", t.TempDir())
	u.turn, u.status = "main-turn", "Working"
	out := notificationTestOutput(t, u)
	btwTestStart(t, u, w)
	questionTestAsync(t, u, "main-question", "Keep Main's question?")
	beforeQuestions, beforeEntries := u.questionCount(), len(u.view.entries)
	if beforeQuestions == 0 {
		t.Fatal("Main's baseline question was not recorded")
	}
	out.Reset()
	for _, message := range []string{
		`{"id":900,"method":"item/tool/requestUserInput","params":{"threadId":"side","turnId":"side-turn"}}`,
		`{"method":"item/completed","params":{"threadId":"side","turnId":"side-turn","item":{"id":"side-question","type":"agentMessage","delivery":"async","questions":[{"title":"Side question?","options":["Yes","No"]}]}}}`,
		`{"method":"thread/tokenUsage/updated","params":{"threadId":"side","turnId":"side-turn","tokenUsage":{"total":{"inputTokens":9999}}}}`,
		`{"method":"turn/completed","params":{"threadId":"side","turn":{"id":"side-turn","status":"completed"}}}`,
	} {
		appServerTestMessage(t, u, message)
	}
	if out.Len() != 0 || u.questionCount() != beforeQuestions || len(u.view.entries) != beforeEntries || u.turn != "main-turn" || u.status != "Working" || u.exitUsage.InputTokens != 0 {
		t.Fatal("side event leaked into Main notification, question, usage, or lifecycle")
	}
}

func TestAppServerBTWAnswerBoundPreservesUTF8(t *testing.T) {
	u, w := newAppServerTestUI()
	btwTestStart(t, u, w)
	chunk := strings.Repeat("界", 90000)
	params, err := json.Marshal(map[string]any{"threadId": "side", "turnId": "side-turn", "itemId": "answer", "delta": chunk})
	if err != nil {
		t.Fatal(err)
	}
	appServerTestMessage(t, u, `{"method":"item/agentMessage/delta","params":`+string(params)+`}`)
	if len(u.btw.answer) != 1 {
		t.Fatalf("answer items = %d", len(u.btw.answer))
	}
	if len(u.btw.answer[0].text) > 256<<10 || !utf8.ValidString(u.btw.answer[0].text) || !u.btw.truncated {
		t.Fatalf("answer bound invalid: bytes=%d valid=%v truncated=%v", len(u.btw.answer[0].text), utf8.ValidString(u.btw.answer[0].text), u.btw.truncated)
	}
}

func TestAppServerBTWCompletionBeforeStartAckKeepsNextDraft(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "/btw first\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
	start := btwTestRequest(t, w, "turn/start", "side")
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"side","turn":{"id":"first-turn"}}}`)
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"side","turn":{"id":"first-turn","status":"completed"}}}`)
	appServerTestKeys(t, u, "/btw second\r")
	if u.draft != "/btw second" || w.Len() != 0 || u.btw.question != "first" {
		t.Fatalf("next side turn sent before first start ack: draft=%q request=%s", u.draft, w.String())
	}
	btwTestReply(t, u, start, `{"turn":{"id":"first-turn"}}`)
	appServerTestKeys(t, u, "\r")
	btwTestRequest(t, w, "turn/start", "side")
}

func TestAppServerBTWCloseInFlightDoesNotAffectReplacement(t *testing.T) {
	for _, stage := range []string{"fork", "start", "active"} {
		t.Run(stage, func(t *testing.T) {
			u, w := newAppServerTestUI()
			shell := &terminalUI{main: u}
			u.shell = shell
			u.turn = "main-turn"
			appServerTestKeys(t, u, "/btw old\r")
			pending := btwTestRequest(t, w, "thread/fork", "main")
			if stage != "fork" {
				btwTestReply(t, u, pending, `{"thread":{"id":"old-side"}}`)
				pending = btwTestRequest(t, w, "turn/start", "old-side")
				if stage == "active" {
					btwTestReply(t, u, pending, `{"turn":{"id":"old-turn"}}`)
				}
			}
			if err := shell.send("\x1b"); err != nil {
				t.Fatal(err)
			}
			if u.btw != nil || u.turn != "main-turn" {
				t.Fatal("Escape failed to close only the side dock")
			}
			var interrupt btwTestRPC
			if stage == "active" {
				interrupt = btwTestRequest(t, w, "turn/interrupt", "old-side")
			} else if w.Len() != 0 {
				t.Fatalf("premature cancellation: %s", w.String())
			}
			appServerTestKeys(t, u, "/btw replacement\r")
			replacement := u.btw
			btwTestRequest(t, w, "thread/fork", "main")
			if stage == "fork" {
				btwTestReply(t, u, pending, `{"thread":{"id":"old-side"}}`)
			} else if stage == "start" {
				btwTestReply(t, u, pending, `{"turn":{"id":"old-turn"}}`)
				interrupt = btwTestRequest(t, w, "turn/interrupt", "old-side")
			}
			if stage != "fork" {
				if interrupt.Params.TurnID != "old-turn" {
					t.Fatalf("wrong interrupt: %+v", interrupt)
				}
				btwTestReply(t, u, interrupt, `{}`)
			}
			unload := btwTestRequest(t, w, "thread/unsubscribe", "old-side")
			btwTestReply(t, u, unload, `{}`)
			appServerTestMessage(t, u, `{"method":"item/agentMessage/delta","params":{"threadId":"old-side","turnId":"old-turn","itemId":"a","delta":"late"}}`)
			if u.btw != replacement || replacement.question != "replacement" || len(replacement.answer) != 0 || u.turn != "main-turn" {
				t.Fatal("late response affected replacement or main")
			}
		})
	}
}

func TestAppServerBTWErrorsStayLocal(t *testing.T) {
	for _, stage := range []string{"fork", "start"} {
		t.Run(stage, func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.turn, u.status = "main-turn", "Working"
			appServerTestKeys(t, u, "/btw question\r")
			r := btwTestRequest(t, w, "thread/fork", "main")
			if stage == "start" {
				btwTestReply(t, u, r, `{"thread":{"id":"side"}}`)
				r = btwTestRequest(t, w, "turn/start", "side")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"side failed"}}`, r.ID))
			if u.btw.busy || !u.btw.alert || !strings.Contains(u.btw.status, "side failed") || u.status != "Working" || u.turn != "main-turn" || len(u.view.entries) != 0 {
				t.Fatalf("error leaked or side stayed busy: %+v", u.btw)
			}
		})
	}
}

func TestAppServerBTWRejectedAttachmentsReturnWithNewDraft(t *testing.T) {
	for _, stage := range []string{"fork", "start"} {
		t.Run(stage, func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.session.cwd = t.TempDir()
			path := filepath.Join(u.session.cwd, "image.png")
			if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
				t.Fatal(err)
			}
			u.draft = "/btw Explain "
			u.insertImage(path)
			u.ownedImages = map[string]bool{path: true}
			u.insertDraft(" ")
			bindComposerFile(u, "@missing.txt", "missing.txt")
			appServerTestKeys(t, u, "\r")
			r := btwTestRequest(t, w, "thread/fork", "main")
			if stage == "start" {
				btwTestReply(t, u, r, `{"thread":{"id":"side"}}`)
				r = btwTestRequest(t, w, "turn/start", "side")
			}
			frame := ansi.Strip(strings.Join(u.btwRows(100, 10), "\n"))
			if !strings.Contains(frame, "File contents omitted") {
				t.Fatalf("missing attachment warning: %s", frame)
			}
			appServerTestKeys(t, u, "new draft")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, r.ID))
			if !strings.HasPrefix(u.draft, "/btw\n") || !strings.Contains(u.draft, "Explain ") || !strings.HasSuffix(u.draft, "new draft") || len(u.images) != 1 || len(u.files) != 1 {
				t.Fatalf("rejected input lost: %q images=%+v files=%+v", u.draft, u.images, u.files)
			}
			if u.draft[u.images[0].start:u.images[0].end] != "[Image 1]" || u.draft[u.files[0].start:u.files[0].end] != "@missing.txt" {
				t.Fatal("restored token offsets changed")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("rejected image removed: %v", err)
			}
			if u.btw.pending.text != "" || w.Len() != 0 {
				t.Fatal("failed side input was automatically resent")
			}
		})
	}
}

func TestAppServerBTWRenderAndScroll(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.btw = &appServerBTW{question: "SIDE_QUESTION", status: "Answering", answer: []btwAnswer{{id: "a", text: "**FIRST_ANSWER**\n\n" + strings.Repeat("middle line\n\n", 30) + "LAST_ANSWER"}}}
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	shell := u.shell
	rows := u.btwRows(60, 10)
	if text := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(text, "LAST_ANSWER") || strings.Contains(text, "FIRST_ANSWER") {
		t.Fatalf("dock does not follow tail: %s", text)
	}
	if err := shell.send("\x1b[5~"); err != nil {
		t.Fatal(err)
	}
	if u.btw.scroll == 0 {
		t.Fatal("PgUp did not scroll dock")
	}
	before := u.btw.scroll
	if err := shell.send("\x1b[6~"); err != nil {
		t.Fatal(err)
	}
	if u.btw.scroll >= before {
		t.Fatal("PgDn did not return toward tail")
	}
	for _, width := range []int{1, 2, 12, 60} {
		for _, height := range []int{0, 1, 2, 8} {
			rows := u.btwRows(width, height)
			if len(rows) > height {
				t.Fatalf("height %d overflow: %d", height, len(rows))
			}
			for _, row := range rows {
				if ansi.StringWidth(row) > width {
					t.Fatalf("width %d overflow: %q", width, row)
				}
			}
		}
	}
	u.draft = "COMPOSER_DRAFT"
	var frame bytes.Buffer
	if err := u.paint(&frame, 80, 30); err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(frame.String())
	question, composer := strings.Index(text, "SIDE_QUESTION"), strings.Index(text, "COMPOSER_DRAFT")
	if question < 0 || composer < 0 || question >= composer {
		t.Fatalf("dock not above composer: %s", text)
	}
}
