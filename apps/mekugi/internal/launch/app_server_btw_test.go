package launch

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	"github.com/yusing/mekugi/testsupport/uisnapshot"

	"github.com/charmbracelet/x/ansi"
)

type btwTestRPC struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params struct {
		ThreadID       string         `json:"threadId"`
		TurnID         string         `json:"turnId"`
		ExpectedTurnID string         `json:"expectedTurnId"`
		Ephemeral      bool           `json:"ephemeral"`
		ExcludeTurns   bool           `json:"excludeTurns"`
		Config         map[string]any `json:"config"`
		Input          []struct {
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

func TestAppServerBTWInheritsInvocationTransport(t *testing.T) {
	for _, effort := range []string{"", "high"} {
		t.Run(effort, func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.statusConfig.Provider, u.reasoningEffort = "mekugi_wrap", effort
			appServerTestKeys(t, u, "/btw question\r")
			r := btwTestRequest(t, w, "thread/fork", "main")
			if _, ok := r.Params.Config["model_providers.mekugi_wrap.supports_websockets"]; ok {
				t.Fatal("fork must inherit invocation transport without overriding its provider")
			}
			if effort != "" && r.Params.Config["model_reasoning_effort"] != effort {
				t.Fatal("fork lost reasoning override")
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
			u.startSession("main", t.TempDir())
			u.attached.presentation.Status.Text = "main status"
			if active {
				u.Turn = "main-turn"
			}
			turn, status, entries, agents, usage := u.Turn, u.attached.presentation.Status.Text, len(u.attached.source.Entries), len(u.attached.session.Roster()), u.exitUsage
			btwTestStart(t, u, w)
			if u.Text != "" {
				t.Fatalf("side draft retained: %q", u.Text)
			}
			appServerTestMessage(t, u, `{"method":"thread/started","params":{"thread":{"id":"side"}}}`)
			appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"side","turn":{"id":"side-turn"}}}`)
			appServerTestMessage(t, u, `{"method":"item/agentMessage/delta","params":{"threadId":"side","turnId":"side-turn","itemId":"answer","delta":"**Side answer**"}}`)
			if len(u.BTW.Answer) != 1 || u.BTW.Answer[0].Text != "**Side answer**" {
				t.Fatalf("stream missing: %+v", u.BTW.Answer)
			}
			appServerTestMessage(t, u, `{"method":"thread/tokenUsage/updated","params":{"threadId":"side","tokenUsage":{"total":{"inputTokens":9999,"outputTokens":123}}}}`)
			appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"side","turn":{"id":"side-turn","status":"completed"}}}`)
			if u.Thread != "main" || u.Turn != turn || u.attached.presentation.Status.Text != status || len(u.attached.source.Entries) != entries || len(u.attached.session.Roster()) != agents || u.exitUsage != usage {
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
	if u.Text != "/btw next question" || w.Len() != 0 || u.BTW.Question != "why?" {
		t.Fatalf("busy submission lost draft or sent: draft=%q wire=%s", u.Text, w.String())
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
	u.Turn, u.attached.presentation.Status.Text = "main-turn", "Working"
	btwTestStart(t, u, w)
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"side","turn":{"id":"side-turn","status":"failed","error":{"message":"side model failed"}}}}`)
	if u.BTW.Busy || !u.BTW.Alert || u.BTW.Status != "side model failed" || u.Turn != "main-turn" || u.attached.presentation.Status.Text != "Working" {
		t.Fatalf("side failure changed Main or lost detail: %+v", u.BTW)
	}
}

func TestAppServerBTWUnsolicitedSideEventsDoNotReachMain(t *testing.T) {
	u, w := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.Turn, u.attached.presentation.Status.Text = "main-turn", "Working"
	out := notificationTestOutput(t, u)
	btwTestStart(t, u, w)
	questionTestAsync(t, u, "main-question", "Keep Main's question?")
	beforeQuestions, beforeEntries := u.questions.Count(u.Thread), len(u.attached.source.Entries)
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
	if out.Len() != 0 || u.questions.Count(u.Thread) != beforeQuestions || len(u.attached.source.Entries) != beforeEntries || u.Turn != "main-turn" || u.attached.presentation.Status.Text != "Working" || u.exitUsage.InputTokens != 0 {
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
	if len(u.BTW.Answer) != 1 {
		t.Fatalf("answer items = %d", len(u.BTW.Answer))
	}
	if len(u.BTW.Answer[0].Text) > 256<<10 || !utf8.ValidString(u.BTW.Answer[0].Text) || !u.BTW.Truncated {
		t.Fatalf("answer bound invalid: bytes=%d valid=%v truncated=%v", len(u.BTW.Answer[0].Text), utf8.ValidString(u.BTW.Answer[0].Text), u.BTW.Truncated)
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
	if u.Text != "/btw second" || w.Len() != 0 || u.BTW.Question != "first" {
		t.Fatalf("next side turn sent before first start ack: draft=%q request=%s", u.Text, w.String())
	}
	btwTestReply(t, u, start, `{"turn":{"id":"first-turn"}}`)
	appServerTestKeys(t, u, "\r")
	btwTestRequest(t, w, "turn/start", "side")
}

func TestAppServerBTWCloseInFlightDoesNotAffectReplacement(t *testing.T) {
	for _, stage := range []string{"fork", "start", "active"} {
		t.Run(stage, func(t *testing.T) {
			u, w := newAppServerTestUI()
			shell := terminalTest(u)
			u.Turn = "main-turn"
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
			if u.BTW != nil || u.Turn != "main-turn" {
				t.Fatal("Escape failed to close only the side dock")
			}
			var interrupt btwTestRPC
			if stage == "active" {
				interrupt = btwTestRequest(t, w, "turn/interrupt", "old-side")
			} else if w.Len() != 0 {
				t.Fatalf("premature cancellation: %s", w.String())
			}
			appServerTestKeys(t, u, "/btw replacement\r")
			replacement := u.BTW
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
			if u.BTW != replacement || replacement.Question != "replacement" || len(replacement.Answer) != 0 || u.Turn != "main-turn" {
				t.Fatal("late response affected replacement or main")
			}
		})
	}
}

func TestAppServerBTWErrorsStayLocal(t *testing.T) {
	for _, stage := range []string{"fork", "start"} {
		t.Run(stage, func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.Turn, u.attached.presentation.Status.Text = "main-turn", "Working"
			appServerTestKeys(t, u, "/btw question\r")
			r := btwTestRequest(t, w, "thread/fork", "main")
			if stage == "start" {
				btwTestReply(t, u, r, `{"thread":{"id":"side"}}`)
				r = btwTestRequest(t, w, "turn/start", "side")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"side failed"}}`, r.ID))
			if u.BTW.Busy || !u.BTW.Alert || !strings.Contains(u.BTW.Status, "side failed") || u.attached.presentation.Status.Text != "Working" || u.Turn != "main-turn" || len(u.attached.source.Entries) != 0 {
				t.Fatalf("error leaked or side stayed busy: %+v", u.BTW)
			}
		})
	}
}

func TestAppServerBTWRejectedAttachmentsReturnWithNewDraft(t *testing.T) {
	for _, stage := range []string{"fork", "start"} {
		t.Run(stage, func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.attached.cwd = t.TempDir()
			path := filepath.Join(u.attached.cwd, "image.png")
			if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
				t.Fatal(err)
			}
			u.Text = "/btw Explain "
			insertImage(&u.attached, u.shell, &u.Editor, &u.questions, &u.approvals, u.clock, u.ownedImages, u.side, path)
			u.ownedImages = map[string]bool{path: true}
			insertDraft(&u.attached, u.Editor, u.shell, u.clock, u.ownedImages, &u.questions, &u.approvals, u.side, " ")
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
			if !strings.HasPrefix(u.Text, "/btw\n") || !strings.Contains(u.Text, "Explain ") || !strings.HasSuffix(u.Text, "new draft") || len(u.Images) != 1 || len(u.Files) != 1 {
				t.Fatalf("rejected input lost: %q images=%+v files=%+v", u.Text, u.Images, u.Files)
			}
			if u.Text[u.Images[0].Start:u.Images[0].End] != "[Image 1]" || u.Text[u.Files[0].Start:u.Files[0].End] != "@missing.txt" {
				t.Fatal("restored token offsets changed")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("rejected image removed: %v", err)
			}
			if u.BTW.Pending.Text != "" || w.Len() != 0 {
				t.Fatal("failed side input was automatically resent")
			}
		})
	}
}

func selectSideThread(t *testing.T, u *appServerUI, thread sessionpresentation.Thread) {
	t.Helper()
	v := u.attached.presentation.View()
	if !displaySession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, false, u.side, u.clock, &u.questions, &u.approvals, false, sessionpresentation.DisplayIntent{Version: v.Version, Thread: thread}) {
		t.Fatal("display rejected")
	}
}

func TestAppServerSideFreshReplacementAndDraftIsolation(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "/side\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	if !u.attached.presentation.DisplayingSide() {
		t.Fatal("fresh side not selected")
	}
	btwTestReply(t, u, fork, `{"thread":{"id":"first-side"}}`)
	if w.Len() != 0 || u.side.Thread != "first-side" {
		t.Fatal("empty side started a turn or lost identity")
	}
	appServerTestKeys(t, u, "side draft")
	selectSideThread(t, u, u.attached.presentation.View().Main)
	appServerTestKeys(t, u, "main draft")
	selectSideThread(t, u, u.attached.presentation.View().SideThread)
	if u.Text != "side draft" || u.attached.conversation.Editor.Text != "main draft" {
		t.Fatal("thread drafts mixed")
	}
	appServerTestKeys(t, u, "\r")
	start := btwTestRequest(t, w, "turn/start", "first-side")
	btwTestReply(t, u, start, `{"turn":{"id":"side-turn"}}`)
	appServerTestKeys(t, u, "/side\r")
	if w.Len() != 0 || u.picker.Kind != sessionpresentation.MenuSideReplace {
		t.Fatal("replacement did not ask")
	}
	appServerTestKeys(t, u, "\r") // Cancel initially selected.
	if w.Len() != 0 || u.side.Thread != "first-side" {
		t.Fatal("cancel changed Side")
	}
	appServerTestKeys(t, u, "/side\r\x1b[B\r")
	lines := bytes.Split(bytes.TrimSpace(w.Bytes()), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("replacement requests = %s", w.String())
	}
	var interrupt, replacement btwTestRPC
	if err := json.Unmarshal(lines[0], &interrupt); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[1], &replacement); err != nil {
		t.Fatal(err)
	}
	w.Reset()
	if interrupt.Method != "turn/interrupt" || interrupt.Params.ThreadID != "first-side" || replacement.Method != "thread/fork" || replacement.Params.ThreadID != "main" {
		t.Fatalf("wrong replacement: %+v %+v", interrupt, replacement)
	}
	btwTestReply(t, u, replacement, `{"thread":{"id":"second-side"}}`)
	btwTestReply(t, u, interrupt, `{}`)
	btwTestRequest(t, w, "thread/unsubscribe", "first-side")
	if u.side.Thread != "second-side" || u.Thread != "main" || u.attached.conversation.Editor.Text != "main draft" {
		t.Fatal("late cleanup crossed identity")
	}
	selectSideThread(t, u, u.attached.presentation.View().Main)
	if u.Text != "main draft" {
		t.Fatal("Main draft not restored")
	}
}

func TestUISnapshotSideHeaderAndTranscript(t *testing.T) {
	u, w := newAppServerTestUI()
	u.startSession("main", "/workspace")
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	u.shell.Transcript.Clock, u.shell.Agents.Clock = u.clock, u.clock
	appServerTestKeys(t, u, "/side\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
	appServerTestKeys(t, u, "First question\r")
	start := btwTestRequest(t, w, "turn/start", "side")
	btwTestReply(t, u, start, `{"turn":{"id":"side-turn"}}`)
	appServerTestNotify(t, u, "item/agentMessage/delta", map[string]any{"threadId": "side", "turnId": "side-turn", "itemId": "a", "delta": "**First answer**"})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "side", "turn": map[string]any{"id": "side-turn", "status": "completed"}})
	for _, width := range []int{100, 40} {
		rows := u.sideFrame(width, 24)
		text := strings.Join(rows, "\n")
		if !strings.Contains(text, "First question") || !strings.Contains(text, "First answer") || !strings.Contains(text, "Side") {
			t.Fatalf("side absent: %s", text)
		}
		uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/side-header-%d.txt", width), text+"\n")
	}
	selectSideThread(t, u, u.attached.presentation.View().Main)
	uisnapshot.Assert(t, "testdata/snapshots/side-header-main.txt", strings.Join(u.sideFrame(100, 24), "\n")+"\n")
	selectSideThread(t, u, u.attached.presentation.View().SideThread)
	appServerTestKeys(t, u, "/side\r")
	uisnapshot.Assert(t, "testdata/snapshots/side-replace.txt", strings.Join(u.sideFrame(100, 24), "\n")+"\n")
}

func TestSideTabClickAndAcquiredInputIsolation(t *testing.T) {
	u, w := newAppServerTestUI()
	u.startSession("main", "/workspace")
	appServerTestKeys(t, u, "/side\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
	intent := editorTestIntent(u)
	selectSideThread(t, u, u.attached.presentation.View().Main)
	if err := u.attached.presentation.AdmitAcquiredInput(intent, u.attached.conversation); err == nil {
		t.Fatal("side editor result admitted into Main")
	}
	frame := strings.Join(u.sideFrame(80, 24), "\n")
	if !strings.Contains(frame, "1 Main  Side") || strings.Contains(frame, "Agents · Side") {
		t.Fatalf("side tab misplaced: %s", frame)
	}
	tab := terminalTest(u).SideTab
	for _, hit := range u.shell.Agents.Hits {
		if hit.Agent == "/side" {
			t.Fatal("Side retained a roster header target")
		}
	}
	if tab.W == 0 {
		t.Fatal("side tab has no hit target")
	}
	for _, suffix := range []string{"M", "m"} {
		if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<0;%d;%d%s", tab.X+tab.W-1, tab.Y+1, suffix)); err != nil {
			t.Fatal(err)
		}
	}
	if !u.attached.presentation.DisplayingSide() {
		t.Fatal("last character of Side did not select it")
	}
	appServerTestKeys(t, u, "\x1b[200~side paste\x1b[201~")
	if u.Text != "side paste" || u.attached.conversation.Editor.Text != "" {
		t.Fatal("paste crossed input destination")
	}
	appServerTestKeys(t, u, "\r")
	request := btwTestRequest(t, w, "turn/start", "side")
	if len(request.Params.Input) < 2 || !strings.Contains(request.Params.Input[0].Text, "Inherited conversation history is reference context") || request.Params.Input[len(request.Params.Input)-1].Text != "side paste" {
		t.Fatalf("side scope/input order: %+v", request.Params.Input)
	}
}

func TestUISnapshotSideTabs(t *testing.T) {
	u, w := newAppServerTestUI()
	u.startSession("main", "/workspace")
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	appServerTestKeys(t, u, "/side\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
	for _, width := range []int{40, 100} {
		var rows []string
		for _, target := range []sessionpresentation.Thread{u.attached.presentation.View().Main, u.attached.presentation.View().SideThread} {
			selectSideThread(t, u, target)
			for _, focus := range []int{0, 3} {
				u.shell.Focus = focus
				u.sideFrame(width, 24)
				rows = append(rows, ansi.Truncate(terminalTest(u).nativeStatus(), width, ""))
				rows = append(rows, u.shell.Agents.NativeRoster(width, 4, now, focus == 3)...)
				rows = append(rows, "plain after tabs")
			}
		}
		uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/side-tabs-%d.txt", width), rows, width)
	}
	// Side navigation does not depend on the optional roster contribution.
	u.shell.RosterLabel = ""
	selectSideThread(t, u, u.attached.presentation.View().Main)
	u.sideFrame(40, 24)
	tab := terminalTest(u).SideTab
	if tab.W == 0 {
		t.Fatal("removing the roster hid Side")
	}
	if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<0;%d;%dM", tab.X+2, tab.Y+1)); err != nil {
		t.Fatal(err)
	}
	if !u.attached.presentation.DisplayingSide() {
		t.Fatal("Side tab did not select its thread without a roster")
	}
	composerShellEscape(t, u)
	u.sideFrame(40, 24)
	if terminalTest(u).SideTab.W != 0 {
		t.Fatal("closed Side retained a tab target")
	}
}

func TestSideMainPromptResolutionIsolation(t *testing.T) {
	for _, secret := range []bool{false, true} {
		t.Run(fmt.Sprint(secret), func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.startSession("main", t.TempDir())
			appServerTestKeys(t, u, "/side\r")
			fork := btwTestRequest(t, w, "thread/fork", "main")
			btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
			selectSideThread(t, u, u.attached.presentation.View().Main)
			u.Turn = "turn"
			questionTestSync(t, u, "prompt", secret)
			openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
			if u.questions.activeCall() == nil {
				t.Fatal("fixture did not open prompt")
			}
			questionTestPaint(t, u, 60)
			appServerTestKeys(t, u, "3main answer")
			selectSideThread(t, u, u.attached.presentation.View().SideThread)
			appServerTestKeys(t, u, "side draft")
			questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "prompt"})
			if !secret {
				h := u.attached.conversation.Editor.InputHistory
				if len(h) == 0 || h[len(h)-1].Text != "main answer" {
					t.Fatalf("Main answer replaced in history by Side content: %+v", h)
				}
			}
			if u.side.Drafts.Editor.Text != "side draft" {
				t.Fatalf("side draft after Main resolution = %q; Main = %q; history=%+v", u.side.Drafts.Editor.Text, u.attached.conversation.Editor.Text, u.attached.conversation.Editor.InputHistory)
			}
		})
	}
}
func TestSideProjectionOrderingAfterFeedTrim(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "/side\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
	appServerTestKeys(t, u, "why?\r")
	start := btwTestRequest(t, w, "turn/start", "side")
	btwTestReply(t, u, start, `{"turn":{"id":"side-turn"}}`)
	addTraffic := func(n int) {
		for i := 0; i < n; i++ {
			seq := u.attached.session.Activity().NextSequence()
			u.attached.childSource.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: seq, Agent: "/root/child", Kind: "text", Text: fmt.Sprint(seq)}}})
		}
	}
	addTraffic(1000)
	appServerTestMessage(t, u, `{"method":"item/agentMessage/delta","params":{"threadId":"side","turnId":"side-turn","itemId":"answer","delta":"side answer"}}`)
	addTraffic(1000)
	u.sideFrame(100, 24)
	var texts []string
	for _, e := range u.attached.childSource.Entries {
		if e.Source.Side {
			texts = append(texts, e.Source.Text)
		}
	}
	if len(texts) != 2 || texts[0] != "why?" || texts[1] != "side answer" {
		t.Fatalf("side order after child traffic: %q", texts)
	}
}

func TestSideSearchResponseKeepsOwner(t *testing.T) {
	u, w := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	appServerTestKeys(t, u, "/side\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
	selectSideThread(t, u, u.attached.presentation.View().Main)
	appServerTestKeys(t, u, "@a")
	search := pickerRequests(t, w)
	if len(search) != 1 || search[0].Method != "fuzzyFileSearch" {
		t.Fatalf("search fixture: %+v", search)
	}
	w.Reset()
	selectSideThread(t, u, u.attached.presentation.View().SideThread)
	pickerReply(t, u, search[0].ID, fmt.Sprintf(`{"files":[{"root":%q,"path":"a.go"}]}`, u.attached.cwd))
	selectSideThread(t, u, u.attached.presentation.View().Main)
	if u.picker.Pending != nil {
		t.Fatalf("Main search is permanently pending after response arrived on Side: %+v; wire=%s", *u.picker.Pending, w.String())
	}
}

func TestSideMainApprovalResolutionIsolation(t *testing.T) {
	u, w := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	appServerTestKeys(t, u, "/side\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
	selectSideThread(t, u, u.attached.presentation.View().Main)
	appServerTestKeys(t, u, "main draft")
	u.Turn = "turn"
	approvalTestCommand(t, u, "approval", map[string]any{"availableDecisions": []string{"accept", "decline"}})
	if !openApprovals(&u.approvals, &u.questions, u.attached.conversation, u.shell, &u.picker, &u.Editor, u.Thread) {
		t.Fatal("fixture did not open approval")
	}
	selectSideThread(t, u, u.attached.presentation.View().SideThread)
	appServerTestKeys(t, u, "side draft")
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "approval"})
	if u.side.Drafts.Editor.Text != "side draft" {
		t.Fatalf("Main approval resolution overwrote Side draft: %q", u.side.Drafts.Editor.Text)
	}
}

func TestStaleSideDisplayPreservesReplaceMenu(t *testing.T) {
	u, w := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	appServerTestKeys(t, u, "/side\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
	selectSideThread(t, u, u.attached.presentation.View().Main)
	stale := sessionpresentation.DisplayIntent{Version: u.attached.presentation.View().Version, Thread: u.attached.presentation.View().SideThread}
	appServerTestKeys(t, u, "/side\r")
	if u.picker.Kind != sessionpresentation.MenuSideReplace {
		t.Fatal("fixture menu absent")
	}
	if displaySession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, false, u.side, u.clock, &u.questions, &u.approvals, false, stale) {
		t.Fatal("stale display accepted")
	}
	if !u.picker.Open || u.picker.Kind != sessionpresentation.MenuSideReplace {
		t.Fatalf("stale display dismissed current Replace menu: open=%v kind=%q", u.picker.Open, u.picker.Kind)
	}
}

func TestSideMainForkReplacementCleanup(t *testing.T) {
	for _, command := range []string{"/fork", "/rewind"} {
		t.Run(command, func(t *testing.T) {
			u, w := newResumeSessionTestUI(t)
			u.reasoningEffort = "high"
			appServerTestKeys(t, u, "/side\r")
			sideFork := btwTestRequest(t, w, "thread/fork", "main")
			appServerTestKeys(t, u, "/btw question\r")
			questionFork := btwTestRequest(t, w, "thread/fork", "main")
			selectSideThread(t, u, u.attached.presentation.View().Main)
			if command == "/rewind" {
				rewindTestTurns(t, u, w)
				appServerTestKeys(t, u, "\r")
			} else {
				appServerTestKeys(t, u, command+"\r")
			}
			fork := resumeTestOne(t, w, "thread/fork")
			appServerTestKeys(t, u, "branch draft")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"model","reasoningEffort":"high","thread":{"id":"branch","cwd":"/work"}}}`, fork.ID))
			resumeTestSettle(t, u, w)
			if u.Thread != "branch" || u.side != nil || u.BTW != nil || u.attached.presentation.View().SideThread.ID != "" || u.Text != "branch draft" {
				t.Fatalf("replacement leaked Side or lost Main input: thread=%s side=%+v draft=%q", u.Thread, u.side, u.Text)
			}
			btwTestReply(t, u, sideFork, `{"thread":{"id":"late-side"}}`)
			btwTestRequest(t, w, "thread/unsubscribe", "late-side")
			btwTestReply(t, u, questionFork, `{"thread":{"id":"late-question"}}`)
			btwTestRequest(t, w, "thread/unsubscribe", "late-question")
			if command == "/rewind" {
				menu := u.attached.presentation.View().Menu
				if menu == nil || menu.Title != "Archive the original session?" || u.picker.archiveTarget != "main" {
					t.Fatal("late Side cleanup changed rewind archive targeting")
				}
				appServerTestKeys(t, u, "\r")
			}
			if w.Len() != 0 || u.Thread != "branch" || u.Text != "branch draft" {
				t.Fatal("late Side cleanup changed the branch or dispatched extra work")
			}
		})
	}
}

func TestSideSwitchRetiresRewindPickerRead(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	rewindTestUser(t, u)
	appServerTestKeys(t, u, "/side\r")
	fork := btwTestRequest(t, w, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"}}`)
	selectSideThread(t, u, u.attached.presentation.View().Main)
	appServerTestKeys(t, u, "/rewind\r")
	read := resumeTestOne(t, w, "thread/read")
	selectSideThread(t, u, u.attached.presentation.View().SideThread)
	appServerTestKeys(t, u, "/side\r")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"main","turns":[]}}}`, read.ID))
	menu := u.attached.presentation.View().Menu
	if menu == nil || menu.Kind != sessionpresentation.MenuSideReplace || menu.Title != "Replace Side?" || len(menu.Choices) != 2 || w.Len() != 0 {
		t.Fatal("late rewind read replaced the Side menu")
	}
}

func TestUISnapshotBTWAlongsideActiveSide(t *testing.T) {
	for _, command := range []string{"/side", "/oneoff inspect"} {
		t.Run(command, func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.clock = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local) }
			appServerTestKeys(t, u, command+"\r")
			fork := btwTestRequest(t, w, "thread/fork", "main")
			btwTestReply(t, u, fork, `{"thread":{"id":"work-side"}}`)
			if command == "/side" {
				appServerTestKeys(t, u, "side task\r")
			}
			start := btwTestRequest(t, w, "turn/start", "work-side")
			btwTestReply(t, u, start, `{"turn":{"id":"work-turn"}}`)
			owner := u.side
			appServerTestKeys(t, u, "/btw explain independently\r")
			fork = btwTestRequest(t, w, "thread/fork", "main")
			btwTestReply(t, u, fork, `{"thread":{"id":"question"}}`)
			start = btwTestRequest(t, w, "turn/start", "question")
			btwTestReply(t, u, start, `{"turn":{"id":"question-turn"}}`)
			if u.side != owner || owner.Turn != "work-turn" || u.BTW == owner || u.BTW.Thread != "question" || !u.attached.presentation.DisplayingSide() {
				t.Fatal("BTW replaced or reused the active Side operation")
			}
			appServerTestMessage(t, u, `{"method":"item/agentMessage/delta","params":{"threadId":"work-side","turnId":"work-turn","itemId":"work-answer","delta":"Side continues working"}}`)
			appServerTestMessage(t, u, `{"method":"item/agentMessage/delta","params":{"threadId":"question","turnId":"question-turn","itemId":"question-answer","delta":"Independent BTW answer"}}`)
			if len(u.BTW.Answer) != 1 || u.BTW.Answer[0].Text != "Independent BTW answer" {
				t.Fatal("BTW output crossed conversations")
			}
			for _, width := range []int{40, 100} {
				u.sideFrame(width, 24)
				rows := terminalTest(u).Frame.Rows
				uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/btw-alongside-%s-%d.txt", owner.Label(), width), rows, width)
			}
			appServerTestKeys(t, u, "kept side draft")
			if err := terminalTest(u).send("\x1b"); err != nil {
				t.Fatal(err)
			}
			interrupt := btwTestRequest(t, w, "turn/interrupt", "question")
			if u.BTW != nil || u.side != owner || owner.Turn != "work-turn" || u.Text != "kept side draft" {
				t.Fatal("closing BTW disturbed Side")
			}
			btwTestReply(t, u, interrupt, `{}`)
			btwTestRequest(t, w, "thread/unsubscribe", "question")

			// The Side operation can close while a new BTW fork is pending.
			u.Editor.Take()
			appServerTestKeys(t, u, "/btw another question\r")
			fork = btwTestRequest(t, w, "thread/fork", "main")
			question := u.BTW
			if err := closeSide(&u.attached, u.shell, &u.Client, &u.Editor, &u.questions, &u.approvals, &u.side, &u.ownedImages); err != nil {
				t.Fatal(err)
			}
			btwTestRequest(t, w, "turn/interrupt", "work-side")
			btwTestReply(t, u, fork, `{"thread":{"id":"new-question"}}`)
			btwTestRequest(t, w, "turn/start", "new-question")
			if u.BTW != question || u.side != nil {
				t.Fatal("closing Side disturbed BTW")
			}
		})
	}
}

func TestBTWFailureRestoresOriginatingSideDraft(t *testing.T) {
	for _, closeOrigin := range []bool{false, true} {
		t.Run(fmt.Sprint(closeOrigin), func(t *testing.T) {
			u, w := newAppServerTestUI()
			appServerTestKeys(t, u, "/side\r")
			fork := btwTestRequest(t, w, "thread/fork", "main")
			btwTestReply(t, u, fork, `{"thread":{"id":"work-side"}}`)
			appServerTestKeys(t, u, "/btw question\r")
			fork = btwTestRequest(t, w, "thread/fork", "main")
			appServerTestKeys(t, u, "new side draft")
			selectSideThread(t, u, u.attached.presentation.View().Main)
			appServerTestKeys(t, u, "main draft")
			if closeOrigin {
				if err := closeSide(&u.attached, u.shell, &u.Client, &u.Editor, &u.questions, &u.approvals, &u.side, &u.ownedImages); err != nil {
					t.Fatal(err)
				}
				btwTestRequest(t, w, "thread/unsubscribe", "work-side")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"message":"fork failed"}}`, fork.ID))
			if closeOrigin {
				if !strings.Contains(u.Text, "question") || !strings.HasSuffix(u.Text, "main draft") {
					t.Fatalf("closed-origin input lost: %q", u.Text)
				}
				return
			}
			if u.Text != "main draft" || !strings.HasPrefix(strings.Join(strings.Fields(u.side.Drafts.Editor.Text), " "), "/btw question") || !strings.HasSuffix(u.side.Drafts.Editor.Text, "new side draft") {
				t.Fatalf("restoration crossed draft owners: main=%q side=%q", u.Text, u.side.Drafts.Editor.Text)
			}
		})
	}
}

func TestBTWOnlyAnimationAndEmptyMainDockGeometry(t *testing.T) {
	for _, displaySide := range []bool{false, true} {
		t.Run(fmt.Sprint(displaySide), func(t *testing.T) {
			u, w := newAppServerTestUI()
			if displaySide {
				appServerTestKeys(t, u, "/side\r")
				fork := btwTestRequest(t, w, "thread/fork", "main")
				btwTestReply(t, u, fork, `{"thread":{"id":"work-side"}}`)
			}
			btwTestStart(t, u, w)
			u.sideFrame(120, 30)
			v := u.attached.presentation.View()
			if !v.BTWRunning || !v.Status.Animating {
				t.Fatal("running BTW did not schedule animation independently of idle Main and Side")
			}
			row := u.shell.Main.BTW.Rect.Y + u.shell.Shell.Layout.Main.Y
			if !strings.Contains(ansi.Strip(terminalTest(u).Frame.Rows[row]), "/btw") {
				t.Fatal("BTW hit rectangle does not start on its painted header")
			}
		})
	}
}

func TestSideRejectedInputRetriesOwnThreadWhileBTWBusy(t *testing.T) {
	for _, command := range []string{"/side", "/oneoff question"} {
		t.Run(command, func(t *testing.T) {
			u, w := newAppServerTestUI()
			appServerTestKeys(t, u, command+"\r")
			fork := btwTestRequest(t, w, "thread/fork", "main")
			btwTestReply(t, u, fork, `{"thread":{"id":"work-side"}}`)
			if command == "/side" {
				appServerTestKeys(t, u, "question\r")
			}
			start := btwTestRequest(t, w, "turn/start", "work-side")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"message":"temporary failure"}}`, start.ID))
			if strings.TrimSpace(u.Text) != "question" {
				t.Fatalf("rejected Side input changed command ownership: %q", u.Text)
			}
			selectSideThread(t, u, u.attached.presentation.View().Main)
			btwTestStart(t, u, w)
			question := u.BTW
			selectSideThread(t, u, u.attached.presentation.View().SideThread)
			appServerTestKeys(t, u, "\r")
			btwTestRequest(t, w, "turn/start", "work-side")
			if u.BTW != question || !question.Busy {
				t.Fatal("retry disturbed the independent BTW operation")
			}
		})
	}
}
