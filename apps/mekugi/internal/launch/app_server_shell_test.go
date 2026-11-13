package launch

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/charmbracelet/x/ansi"
)

func TestAppServerShellSubmission(t *testing.T) {
	for _, busy := range []bool{false, true} {
		for _, key := range []string{"\r", "\t"} {
			t.Run(fmt.Sprintf("busy=%v/key=%q", busy, key), func(t *testing.T) {
				u, w := newAppServerTestUI()
				if busy {
					appServerTestTurn(t, u, "model")
				}
				appServerTestKeys(t, u, "!printf '%s' \"$HOME\" | cat > result"+key)
				if busy && key == "\t" {
					if w.Len() != 0 || u.Text != "" || len(u.attached.conversation.Queued) != 1 {
						t.Fatalf("busy Tab executed shell: wire=%s draft=%q", w.String(), u.Text)
					}
					appServerTestTurnEnd(t, u, "model", "completed")
				}
				var request struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
					Params struct {
						Thread  string `json:"threadId"`
						Command string `json:"command"`
					} `json:"params"`
				}
				if err := json.Unmarshal(w.Bytes(), &request); err != nil {
					t.Fatal(err)
				}
				w.Reset()
				if request.Method != "thread/shellCommand" || request.Params.Thread != "main" || request.Params.Command != "printf '%s' \"$HOME\" | cat > result" {
					t.Fatalf("shell request: %+v", request)
				}
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
				if u.Text != "" || len(u.InputHistory) != 1 || len(appServerTurnRequests(t, w)) != 0 {
					t.Fatal("acknowledgement started model input or lost history")
				}
				if busy && key != "\t" {
					appServerTestMessage(t, u, `{"method":"item/started","params":{"threadId":"main","turnId":"model","item":{"id":"shell","type":"commandExecution","source":"userShell","command":"pwd","status":"inProgress"}}}`)
					if u.Turn != "model" || u.Shell.Standalone() {
						t.Fatal("shell replaced main turn")
					}
					appServerTestKeys(t, u, "follow up\r")
					appServerOneRequest(t, w, "turn/steer", "follow up")
				} else {
					appServerTestTurn(t, u, "shell")
					appServerTestKeys(t, u, "follow up\r")
					if len(appServerTurnRequests(t, w)) != 0 {
						t.Fatal("ordinary input steered shell turn")
					}
					appServerTestTurnEnd(t, u, "shell", "completed")
					appServerOneRequest(t, w, "turn/start", "follow up")
				}
			})
		}
	}
}

func TestAppServerShellIdleCompletionDoesNotStartModel(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "!pwd\r")
	w.Reset()
	appServerTestTurn(t, u, "shell")
	appServerTestTurnEnd(t, u, "shell", "completed")
	// Completion may race ahead of the RPC acknowledgement.
	appServerTestMessage(t, u, `{"id":1,"result":{}}`)
	if u.Shell.Standalone() || u.Starting() || u.Turn != "" || len(appServerTurnRequests(t, w)) != 0 {
		t.Fatal("idle shell completion started a model turn")
	}
}

func TestAppServerShellWaitsForCompaction(t *testing.T) {
	for _, phase := range []string{"running", "completion-before-ack", "prerequisite-completion-before-ack"} {
		t.Run(phase, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			prerequisite := strings.HasPrefix(phase, "prerequisite")
			if prerequisite {
				appServerTestTurn(t, u, "work")
			}
			appServerTestKeys(t, u, "/compact\r")
			request := appServerOneRequest(t, wire, "thread/compact/start", "")
			acknowledgement := fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID)
			if phase == "running" {
				appServerTestMessage(t, u, acknowledgement)
			}
			if prerequisite {
				appServerTestTurnEnd(t, u, "work", "interrupted")
			} else {
				compactTestStart(t, u, request, "compact", 1)
				if phase == "completion-before-ack" {
					compactTestEnd(t, u, request, "compact", "completed", 1)
				}
			}
			appServerTestKeys(t, u, "!pwd\r")
			if wire.Len() != 0 || u.Text != "!pwd" || u.attached.feedback.Text == "" {
				t.Fatalf("shell overtook compaction: wire=%s draft=%q notice=%q", wire.String(), u.Text, u.attached.feedback.Text)
			}
			if prerequisite {
				return // Existing busy-turn shell coverage checks ordinary Main admission.
			}
			if phase == "running" {
				compactTestEnd(t, u, request, "compact", "completed", 1)
			} else {
				appServerTestMessage(t, u, acknowledgement)
			}
			appServerTestKeys(t, u, "\r")
			btwTestRequest(t, wire, "thread/shellCommand", "main")
		})
	}
}

func TestAppServerShellRejectedAndEmpty(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "!\r")
	if w.Len() != 0 || u.Text != "!" || u.attached.feedback.Text == "" {
		t.Fatal("empty shell command was submitted or silently discarded")
	}
	appServerTestKeys(t, u, "pwd\rnext")
	w.Reset()
	appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"unavailable"}}`)
	if u.Text != "next" || !u.attached.feedback.Alert || u.Starting() || u.Shell.Standalone() || w.Len() != 0 || len(u.InputHistory) != 1 || u.InputHistory[0].Text != "!pwd" {
		t.Fatalf("rejected shell lost input or blocked composer: %+v", u)
	}
	appServerTestKeys(t, u, "\x1b[A\r")
	if strings.Contains(w.String(), "next") || !strings.Contains(w.String(), `"command":"pwd"`) {
		t.Fatalf("shell retry included ordinary draft: %s", w.String())
	}
}

func TestAppServerShellMainCompletionRace(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestTurn(t, u, "model")
	appServerTestKeys(t, u, "!pwd\r")
	w.Reset()
	appServerTestMessage(t, u, `{"id":1,"result":{}}`)
	appServerTestTurnEnd(t, u, "model", "completed")
	appServerTestKeys(t, u, "follow up\r")
	if w.Len() != 0 {
		t.Fatal("follow-up raced shell startup")
	}
	appServerTestTurn(t, u, "shell")
	if !u.Shell.Standalone() || w.Len() != 0 {
		t.Fatal("observed standalone shell misclassified")
	}
	appServerTestTurnEnd(t, u, "shell", "completed")
	appServerOneRequest(t, w, "turn/start", "follow up")
}

func TestAppServerShellOlderCompletionKeepsNewOrigin(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	w := u.Host.Input.(*appServerTestInput)
	appServerTestTurn(t, u, "model")
	appServerTestKeys(t, u, "!pwd\r")
	appServerTestMessage(t, u, `{"id":1,"result":{}}`)
	appServerTestMessage(t, u, `{"method":"item/started","params":{"threadId":"main","turnId":"model","item":{"id":"a","type":"commandExecution","source":"userShell","command":"pwd","status":"inProgress"}}}`)
	appServerTestKeys(t, u, "!date\r")
	appServerTestMessage(t, u, `{"id":2,"result":{}}`)
	w.Reset()
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"model","item":{"id":"a","type":"commandExecution","source":"userShell","command":"pwd","status":"completed"}}}`)
	if u.AcceptsShell() {
		t.Fatal("older completion settled newer shell submission")
	}
	appServerTestTurnEnd(t, u, "model", "completed")
	appServerTestTurn(t, u, "shell")
	appServerTestKeys(t, u, "follow up\r")
	if w.Len() != 0 || !u.Shell.Standalone() {
		t.Fatal("follow-up steered standalone shell")
	}
	appServerTestTurnEnd(t, u, "shell", "completed")
	appServerOneRequest(t, w, "turn/start", "follow up")
}

func TestAppServerShellComposer(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "!echo $HOME")
	if u.picker.completionTarget(u.Draft, u.attached.conversation.ShellMode()).Kind != 0 || w.Len() != 0 {
		t.Fatal("shell variable opened a picker")
	}
	frame, _ := u.testMainFrame(80, 20, 0)
	text := strings.Join(frame, "\n")
	redraw, _ := u.testMainFrame(80, 20, 0)
	if strings.Join(redraw, "\n") != text {
		t.Fatal("redrawing the shell composer changed its text or cursor")
	}
	if !strings.Contains(ansi.Strip(text), "Shell Mode") || !strings.Contains(text, terminalui.Red+"╭") || !strings.Contains(text, terminalui.Red+"│") {
		t.Fatalf("shell notice/frame missing: %q", text)
	}
	u.nativeSession.deleteDraftRange(&u.attached, &u.picker, &u.Editor, u.side, u.clock, u.statusPanel != nil || u.resumePicker != nil, 0, 1)
	if u.attached.conversation.ShellMode() {
		t.Fatal("removing bang did not leave shell mode")
	}
	nativeTestCancel(t, u)
	appServerTestKeys(t, u, "\x1b[200~!printf '%s\\n' \"你好\n$HOME\"\n\x1b[201~")
	appServerTestKeys(t, u, "\x1b[A\x1b[H")
	frame, _ = u.testMainFrame(18, 20, 0)
	if u.Cursor() != strings.Index(u.Text, "$HOME") || !strings.Contains(ansi.Strip(strings.Join(frame, "\n")), "$HOME") {
		t.Fatal("multiline navigation or wrapped command changed")
	}
	appServerTestKeys(t, u, "x\r")
	if !strings.Contains(w.String(), `"command":"printf '%s\\n' \"你好\nx$HOME\""`) || u.Text != "" {
		t.Fatalf("paint/edit changed shell submission: %s draft=%q", w.String(), u.Text)
	}
}

func TestAppServerQueuedShellFIFO(t *testing.T) {
	for _, completionFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(completionFirst), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.guardHookCheck.hash = "guard"
			appServerTestTurn(t, u, "model")
			appServerTestKeys(t, u, "!printf first\t!printf second\tordinary input\t")
			if wire.Len() != 0 || len(u.attached.conversation.Queued) != 3 {
				t.Fatal("busy shell queue executed early")
			}
			appServerTestTurnEnd(t, u, "model", "completed")
			for i, command := range []string{"printf first", "printf second"} {
				var shell struct {
					Params struct {
						Command string `json:"command"`
					} `json:"params"`
				}
				if err := json.Unmarshal(wire.Bytes(), &shell); err != nil || shell.Params.Command != command {
					t.Fatalf("shell order: params=%+v err=%v", shell.Params, err)
				}
				request := appServerOneRequest(t, wire, "thread/shellCommand", "")
				turn := fmt.Sprintf("shell-%d", i)
				ack := fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID)
				if !completionFirst {
					appServerTestMessage(t, u, ack)
				}
				appServerTestTurn(t, u, turn)
				if wire.Len() != 0 {
					t.Fatal("queue overtook shell execution")
				}
				appServerTestTurnEnd(t, u, turn, "completed")
				if completionFirst {
					if wire.Len() != 0 {
						t.Fatal("queue overtook shell acknowledgement")
					}
					appServerTestMessage(t, u, ack)
				}
			}
			check := appServerOneRequest(t, wire, "hooks/list", "")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":%s}`, check.ID, guardHookTestResult(t, u.attached.cwd, guardHookTestEntry("guard"))))
			appServerOneRequest(t, wire, "turn/start", "ordinary input")
		})
	}
}

func TestAppServerQueuedShellRetractionAndRejection(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint(mixed), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			appServerTestTurn(t, u, "model")
			appServerTestKeys(t, u, "!pwd\t")
			if mixed {
				appServerTestKeys(t, u, "prose\t")
			}
			editQueued(&u.attached, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock)
			if wire.Len() != 0 || len(u.attached.conversation.Queued) != 0 {
				t.Fatal("retraction executed shell")
			}
			if mixed {
				if u.Text != "prose" || len(u.InputHistory) != 1 || u.InputHistory[0].Text != "!pwd" {
					t.Fatal("shell joined prose on restoration")
				}
				return
			}
			if u.Text != "!pwd" {
				t.Fatalf("shell draft not restored: %q", u.Text)
			}
			appServerTestKeys(t, u, "\t")
			appServerTestTurnEnd(t, u, "model", "completed")
			request := appServerOneRequest(t, wire, "thread/shellCommand", "")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, request.ID))
			if u.Text != "!pwd" || wire.Len() != 0 || !u.attached.feedback.Alert {
				t.Fatal("queued shell rejection lost retry draft")
			}
		})
	}
}

func TestAppServerQueuedShellWaitsForLogicalWork(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.LocalStop = "logical-wait"
	appServerTestKeys(t, u, "!pwd\t")
	if err := u.nativeSession.flushInput(&u.attached, &u.picker, u.clock, &u.Editor, u.ctx); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 || len(u.attached.conversation.Queued) != 1 {
		t.Fatal("queued shell superseded logical wait")
	}
	u.LocalStop = ""
	appServerTestTurn(t, u, "resumed")
	if wire.Len() != 0 {
		t.Fatal("queued shell steered resumed work")
	}
	appServerTestTurnEnd(t, u, "resumed", "completed")
	appServerOneRequest(t, wire, "thread/shellCommand", "")
}

func TestAppServerQueuedShellFollowsQueuedMessage(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestTurn(t, u, "model")
	appServerTestKeys(t, u, "first message\t!pwd\tlast message\t")
	appServerTestTurnEnd(t, u, "model", "completed")
	request := appServerOneRequest(t, wire, "turn/start", "first message")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID))
	appServerTestTurn(t, u, "first")
	if wire.Len() != 0 {
		t.Fatal("shell overtook queued model turn")
	}
	appServerTestTurnEnd(t, u, "first", "completed")
	appServerOneRequest(t, wire, "thread/shellCommand", "")
	if len(u.attached.conversation.Queued) != 1 || u.attached.conversation.Queued[0].Text != "last message" {
		t.Fatal("shell consumed subsequent prose")
	}
}

func TestAppServerQueuedShellRejectsEmptyAndAttachments(t *testing.T) {
	for _, text := range []string{"!", "!pwd"} {
		u, wire := newAppServerTestUI()
		appServerTestTurn(t, u, "model")
		appServerTestKeys(t, u, text)
		if text != "!" {
			u.Images = []composer.Token{{Path: "image.png"}}
		}
		appServerTestKeys(t, u, "\t")
		if wire.Len() != 0 || u.Text != text || len(u.attached.conversation.Queued) != 0 || u.attached.feedback.Text == "" {
			t.Fatal("invalid shell draft was consumed")
		}
	}
}
