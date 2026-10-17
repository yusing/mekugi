package router

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/terminalui"

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
				if busy {
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
			method := "thread/compact/start"
			if prerequisite {
				method = "turn/interrupt"
			}
			request := appServerOneRequest(t, wire, method, "")
			acknowledgement := fmt.Sprintf(`{"id":%d,"result":{}}`, request.ID)
			if phase == "running" {
				appServerTestMessage(t, u, acknowledgement)
			}
			if prerequisite {
				appServerTestTurnEnd(t, u, "work", "interrupted")
			} else {
				appServerTestTurn(t, u, "compact")
				if phase == "completion-before-ack" {
					appServerTestTurnEnd(t, u, "compact", "completed")
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
				appServerTestTurnEnd(t, u, "compact", "completed")
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
	if u.completionTarget().Kind != 0 || w.Len() != 0 {
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
	u.deleteDraftRange(0, 1)
	if u.attached.conversation.ShellMode() {
		t.Fatal("removing bang did not leave shell mode")
	}
	appServerTestKeys(t, u, "\x03\x1b[200~!printf '%s\\n' \"你好\n$HOME\"\n\x1b[201~")
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
