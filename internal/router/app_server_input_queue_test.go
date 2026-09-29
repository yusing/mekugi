package router

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

type appServerTurnRequest struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params struct {
		ExpectedTurnID      string `json:"expectedTurnId"`
		ClientUserMessageID string `json:"clientUserMessageId"`
		Input               []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Path string `json:"path"`
		} `json:"input"`
	} `json:"params"`
}

func (r appServerTurnRequest) text() string {
	var text string
	for _, input := range r.Params.Input {
		text += input.Text
	}
	return text
}

// appServerTurnRequests drains the requests written since the last call.
func appServerTurnRequests(t *testing.T, w *appServerTestInput) []appServerTurnRequest {
	t.Helper()
	var requests []appServerTurnRequest
	for line := range bytes.SplitSeq(bytes.TrimSpace(w.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var request appServerTurnRequest
		if err := json.Unmarshal(line, &request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
	}
	w.Reset()
	return requests
}

func appServerOneRequest(t *testing.T, w *appServerTestInput, method, text string) appServerTurnRequest {
	t.Helper()
	requests := appServerTurnRequests(t, w)
	if len(requests) != 1 || requests[0].Method != method || requests[0].text() != text {
		t.Fatalf("requests = %+v, want one %s %q", requests, method, text)
	}
	return requests[0]
}

func appServerTestTurn(t *testing.T, u *appServerUI, turn string) {
	t.Helper()
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"`+turn+`"}}}`)
}

func appServerTestTurnEnd(t *testing.T, u *appServerUI, turn, status string) {
	t.Helper()
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"`+turn+`","status":"`+status+`"}}}`)
}

func appServerTestUserMessage(t *testing.T, u *appServerUI, id, clientID, text string) {
	t.Helper()
	appServerTestMessage(t, u, fmt.Sprintf(`{"method":"item/completed","params":{"threadId":"main","turnId":"t","item":{"id":%q,"clientId":%q,"type":"userMessage","content":[{"type":"text","text":%q}]}}}`, id, clientID, text))
}

func TestAppServerSteersStackUntilSendable(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "t")
	appServerTestKeys(t, u, "first\r")
	first := appServerOneRequest(t, w, "turn/steer", "first")
	if first.Params.ExpectedTurnID != "t" || first.Params.ClientUserMessageID == "" {
		t.Fatalf("steer identity: %+v", first.Params)
	}
	appServerTestKeys(t, u, "second\rthird\r")
	if requests := appServerTurnRequests(t, w); len(requests) != 0 || len(u.unsent) != 2 {
		t.Fatalf("steer sent while another was unresolved: %+v unsent=%d", requests, len(u.unsent))
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, first.ID))
	stacked := appServerOneRequest(t, w, "turn/steer", "second\nthird")
	if stacked.Params.ExpectedTurnID != "t" || stacked.Params.ClientUserMessageID == first.Params.ClientUserMessageID {
		t.Fatalf("stacked steer identity: %+v", stacked.Params)
	}
	preview := ansi.Strip(strings.Join(u.pendingInputPreview(80), "\n"))
	if !strings.Contains(preview, "Steering after the next tool call") || !strings.Contains(preview, "↳ first") || !strings.Contains(preview, "↳ second") {
		t.Fatalf("pending steers not previewed: %q", preview)
	}
	// The committed message leaves the preview for the transcript, matched
	// by its client ID even before the steer's acknowledgement.
	appServerTestUserMessage(t, u, "user-1", first.Params.ClientUserMessageID, "first")
	appServerTestUserMessage(t, u, "user-2", stacked.Params.ClientUserMessageID, "second\nthird")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, stacked.ID))
	if len(u.steers) != 0 || u.submission.text != "" || len(u.pendingInputPreview(80)) != 0 {
		t.Fatalf("committed steers still pending: %+v", u.steers)
	}
	if len(u.view.entries) != 2 || u.view.entries[1].Text != "second\nthird" {
		t.Fatalf("committed steers missing from transcript: %+v", u.view.entries)
	}
	appServerTestTurnEnd(t, u, "t", "completed")
	if u.draft != "" || len(appServerTurnRequests(t, w)) != 0 {
		t.Fatal("committed steers were restored or resent")
	}
}

func TestAppServerQueuedInputStacksIntoNextTurn(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "t")
	appServerTestKeys(t, u, "one\ttwo\t")
	if requests := appServerTurnRequests(t, w); len(requests) != 0 || len(u.queued) != 2 || u.draft != "" {
		t.Fatalf("queued input sent early: %+v", requests)
	}
	frame, _ := u.mainFrame(80, 20, 0)
	if len(frame) != 20 {
		t.Fatalf("frame height = %d", len(frame))
	}
	// A blank row separates the preview from the composer's top border.
	above := ansi.Strip(strings.Join(frame[len(frame)-7:len(frame)-4], "\n"))
	if frame[len(frame)-4] != "" || above != "• Queued for the next turn · alt+↑ edits\n  ↳ one\n  ↳ two" || !strings.HasPrefix(frame[len(frame)-3], "\x1b[38;2;52;48;72m╭") {
		t.Fatalf("queued input not previewed above composer: %q", above)
	}
	appServerTestTurnEnd(t, u, "t", "completed")
	start := appServerOneRequest(t, w, "turn/start", "one\ntwo")
	if len(u.queued) != 0 || !u.starting || len(u.view.entries) != 1 || u.view.entries[0].Text != "one\ntwo" {
		t.Fatalf("queued turn not started and echoed: %+v", u.view.entries)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":"t2"}}}`, start.ID))
	if len(u.inputHistory) != 2 || u.inputHistory[0].text != "one" {
		t.Fatalf("history does not keep each entry: %+v", u.inputHistory)
	}

	// Idle Tab sends like Enter.
	u, w = newAppServerTestUI()
	appServerTestKeys(t, u, "now\t")
	appServerOneRequest(t, w, "turn/start", "now")
}

func TestAppServerQueuedAttachmentsKeepFilesUntilSent(t *testing.T) {
	u, w := newAppServerTestUI()
	path := filepath.Join(t.TempDir(), "queued.png")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	appServerTestTurn(t, u, "t")
	u.attachImage(path)
	appServerTestKeys(t, u, " see\t")
	appServerTestKeys(t, u, "other\x7f")
	if _, err := os.Stat(path); err != nil || !u.ownedImages[path] {
		t.Fatalf("queued image reclaimed: %v", err)
	}
	appServerTestKeys(t, u, "\x03\x03") // Clear the draft, then interrupt: queued input returns.
	appServerTestTurnEnd(t, u, "t", "interrupted")
	if u.draft != "[Image 1] see" || len(u.images) != 1 || u.images[0].path != path {
		t.Fatalf("interrupt did not restore queued attachment: %q %+v", u.draft, u.images)
	}
	appServerTurnRequests(t, w)
	appServerTestKeys(t, u, "\r")
	request := appServerOneRequest(t, w, "turn/start", " see")
	if request.Params.Input[0].Path != path || u.ownedImages[path] {
		t.Fatalf("sent image not handed to Codex: %+v", request.Params.Input)
	}
}

func TestAppServerInterruptRestoresSteersAndQueuedInput(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "t")
	appServerTestKeys(t, u, "accepted\r")
	accepted := appServerOneRequest(t, w, "turn/steer", "accepted")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, accepted.ID))
	appServerTestKeys(t, u, "later\t\x03")
	appServerOneRequest(t, w, "turn/interrupt", "")
	appServerTestKeys(t, u, "typed while interrupting\r")
	if requests := appServerTurnRequests(t, w); len(requests) != 0 {
		t.Fatalf("steered the interrupted turn: %+v", requests)
	}
	appServerTestTurnEnd(t, u, "t", "interrupted")
	if requests := appServerTurnRequests(t, w); len(requests) != 0 || u.draft != "accepted\ntyped while interrupting\nlater" || len(u.queued)+len(u.unsent) != 0 {
		t.Fatalf("input not restored: requests=%+v draft=%q", requests, u.draft)
	}
}

func TestAppServerInterruptRestoresUnresolvedSteerOnce(t *testing.T) {
	for _, tt := range []struct {
		name, response string
		committed      bool
	}{
		{"rejected after interrupt", `{"code":-1,"message":"no active turn to steer"}`, false},
		{"accepted after interrupt", "", false},
		{"committed before interrupt", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, w := newAppServerTestUI()
			appServerTestTurn(t, u, "t")
			appServerTestKeys(t, u, "unresolved\r")
			steer := appServerOneRequest(t, w, "turn/steer", "unresolved")
			if tt.committed {
				appServerTestUserMessage(t, u, "user-1", steer.Params.ClientUserMessageID, "unresolved")
			}
			appServerTestKeys(t, u, "\x03")
			appServerOneRequest(t, w, "turn/interrupt", "")
			appServerTestTurnEnd(t, u, "t", "interrupted")
			if requests := appServerTurnRequests(t, w); len(requests) != 0 {
				t.Fatalf("sent before the steer resolved: %+v", requests)
			}
			if tt.response != "" {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":%s}`, steer.ID, tt.response))
			} else {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, steer.ID))
			}
			want := "unresolved"
			if tt.committed {
				want = ""
			}
			if requests := appServerTurnRequests(t, w); len(requests) != 0 || u.draft != want || u.alert {
				t.Fatalf("requests=%+v draft=%q alert=%v status=%q", requests, u.draft, u.alert, u.status)
			}
		})
	}
}

func TestAppServerPlainInterruptRestoresQueuedInput(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "t")
	appServerTestKeys(t, u, "queued\t\x03")
	appServerOneRequest(t, w, "turn/interrupt", "")
	if u.status != "Interrupting…" {
		t.Fatalf("status = %q", u.status)
	}
	appServerTestKeys(t, u, "draft")
	appServerTestTurnEnd(t, u, "t", "interrupted")
	if u.draft != "queued\ndraft" || len(u.queued) != 0 || len(appServerTurnRequests(t, w)) != 0 {
		t.Fatalf("queued input not returned to composer: %q", u.draft)
	}
}

func TestAppServerEditLastQueuedInput(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "t")
	appServerTestKeys(t, u, "one\ttwo\tdraft")
	appServerTestKeys(t, u, "\x1b[1;3A")
	if u.draft != "two\ndraft" || len(u.queued) != 1 || u.queued[0].text != "one" {
		t.Fatalf("alt+up edited %q, queued %+v", u.draft, u.queued)
	}
	appServerTestKeys(t, u, "\x1b[1;2D")
	if u.draft != "one\ntwo\ndraft" || len(u.queued) != 0 || w.Len() != 0 {
		t.Fatalf("shift+left edited %q", u.draft)
	}
}

func TestAppServerUnconfirmedSteerReturnsAfterTurnEnds(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "t")
	appServerTestKeys(t, u, "unconfirmed\r")
	steer := appServerOneRequest(t, w, "turn/steer", "unconfirmed")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, steer.ID))
	appServerTestTurnEnd(t, u, "t", "completed")
	if u.draft != "unconfirmed" || len(u.steers) != 0 || len(appServerTurnRequests(t, w)) != 0 {
		t.Fatalf("unconfirmed steer resent or lost: draft=%q", u.draft)
	}
}

func TestAppServerInputStackedDuringSettingsSendsAfterApply(t *testing.T) {
	u, w := newAppServerTestUI()
	u.reasoningEffort = "low"
	appServerTestKeys(t, u, "/reasoning high\r")
	appServerOneRequest(t, w, "thread/settings/update", "")
	appServerTestKeys(t, u, "prompt\r")
	if len(appServerTurnRequests(t, w)) != 0 || !strings.Contains(ansi.Strip(strings.Join(u.pendingInputPreview(80), "\n")), "Sending when settings apply") {
		t.Fatal("prompt not held while settings apply")
	}
	appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"main","threadSettings":{"effort":"high"}}}`)
	appServerOneRequest(t, w, "turn/start", "prompt")
}

func TestAppServerEndedTurnKeepsSteerOrder(t *testing.T) {
	for _, tt := range []struct {
		name        string
		interrupt   bool
		response    string
		wantRequest string
		wantDraft   string
	}{
		{"interrupt restore after rejection", true, `"error":{"code":-1,"message":"no active turn to steer"}`, "", "A\nB\nC"},
		{"interrupt restore after acceptance", true, `"result":{"turnId":"t"}`, "", "A\nB\nC"},
		{"turn ended", false, `"result":{"turnId":"t"}`, "C", "A\nB"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, w := newAppServerTestUI()
			appServerTestTurn(t, u, "t")
			appServerTestKeys(t, u, "A\r")
			a := appServerOneRequest(t, w, "turn/steer", "A")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, a.ID))
			appServerTestKeys(t, u, "B\r")
			b := appServerOneRequest(t, w, "turn/steer", "B")
			appServerTestKeys(t, u, "C\r")
			status := "completed"
			if tt.interrupt {
				appServerTestKeys(t, u, "\x03")
				appServerOneRequest(t, w, "turn/interrupt", "")
				status = "interrupted"
			}
			appServerTestTurnEnd(t, u, "t", status)
			if requests := appServerTurnRequests(t, w); len(requests) != 0 {
				t.Fatalf("settled before the in-flight steer resolved: %+v", requests)
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,%s}`, b.ID, tt.response))
			if tt.wantRequest != "" {
				appServerOneRequest(t, w, "turn/start", tt.wantRequest)
			} else if requests := appServerTurnRequests(t, w); len(requests) != 0 {
				t.Fatalf("unexpected resend: %+v", requests)
			}
			if u.draft != tt.wantDraft {
				t.Fatalf("draft = %q, want %q", u.draft, tt.wantDraft)
			}
		})
	}
}

func TestAppServerRejectedStartReturnsInputStackedBehindIt(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "A\r")
	start := appServerOneRequest(t, w, "turn/start", "A")
	appServerTestKeys(t, u, "steer\rB\t")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, start.ID))
	if requests := appServerTurnRequests(t, w); len(requests) != 0 || u.draft != "A\nsteer\nB" || len(u.unsent)+len(u.queued) != 0 {
		t.Fatalf("stacked input jumped a rejected start: %+v draft=%q", requests, u.draft)
	}
}

func TestAppServerTextFallbackCommitsOneIdenticalSteer(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "t")
	for range 2 {
		appServerTestKeys(t, u, "yes\r")
		steer := appServerOneRequest(t, w, "turn/steer", "yes")
		appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"t"}}`, steer.ID))
	}
	for _, method := range []string{"item/started", "item/completed"} {
		appServerTestMessage(t, u, `{"method":"`+method+`","params":{"threadId":"main","turnId":"t","item":{"id":"user-1","type":"userMessage","content":[{"type":"text","text":"yes"}]}}}`)
	}
	if len(u.steers) != 1 {
		t.Fatalf("one commit settled %d steers", 2-len(u.steers))
	}
}

func TestAppServerRestoreKeepsCaret(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestTurn(t, u, "t")
	appServerTestKeys(t, u, "queued\tabcd\x1b[D\x1b[D\x1b[1;3A")
	if u.draft != "queued\nabcd" || u.cursor() != len("queued\nab") {
		t.Fatalf("draft=%q caret=%d", u.draft, u.cursor())
	}
}

func TestAppServerInterruptBeforeFirstCommitRestoresCleanComposer(t *testing.T) {
	for _, responseFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(responseFirst), func(t *testing.T) {
			u, w := newAppServerTestUI()
			appServerTestKeys(t, u, "first prompt\r")
			start := appServerOneRequest(t, w, "turn/start", "first prompt")
			if responseFirst {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":"t"}}}`, start.ID))
			}
			appServerTestKeys(t, u, "\x03")
			if requests := appServerTurnRequests(t, w); len(requests) != 0 {
				t.Fatalf("interrupted without a host turn: %+v", requests)
			}
			appServerTestTurn(t, u, "t")
			appServerOneRequest(t, w, "turn/interrupt", "")
			appServerTestTurnEnd(t, u, "t", "interrupted")
			if !responseFirst {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":"t"}}}`, start.ID))
			}
			if u.draft != "first prompt" || len(u.view.entries) != 0 || u.status != "Ready" || u.starting || u.turn != "" {
				t.Fatalf("not clean: draft=%q entries=%+v status=%q starting=%v turn=%q", u.draft, u.view.entries, u.status, u.starting, u.turn)
			}
			if len(appServerTurnRequests(t, w)) != 0 {
				t.Fatal("automatically resent restored input")
			}
			var frame bytes.Buffer
			if err := u.paint(&frame, 80, 24); err != nil {
				t.Fatal(err)
			}
			if got := appServerComposerScreenText(t, frame.Bytes(), 80, 24); strings.TrimSpace(got) != "first prompt" {
				t.Fatalf("composer frame = %q", got)
			}
		})
	}
}

func TestAppServerInterruptLocallyStackedInputDoesNotQuit(t *testing.T) {
	u, w := newAppServerTestUI()
	u.settingsPending = true
	appServerTestKeys(t, u, "not sent\r")
	quit, err := u.key(3)
	if quit || err != nil || u.draft != "not sent" || len(u.unsent) != 0 || w.Len() != 0 {
		t.Fatalf("quit=%v err=%v draft=%q requests=%s", quit, err, u.draft, w.String())
	}
}
