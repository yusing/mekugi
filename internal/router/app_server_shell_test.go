package router

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
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
				if u.draft != "" || len(u.inputHistory) != 1 || len(appServerTurnRequests(t, w)) != 0 {
					t.Fatal("acknowledgement started model input or lost history")
				}
				if busy {
					appServerTestMessage(t, u, `{"method":"item/started","params":{"threadId":"main","turnId":"model","item":{"id":"shell","type":"commandExecution","source":"userShell","command":"pwd","status":"inProgress"}}}`)
					if u.turn != "model" || u.shellStandalone {
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
	if u.shellStandalone || u.starting || u.turn != "" || len(appServerTurnRequests(t, w)) != 0 {
		t.Fatal("idle shell completion started a model turn")
	}
}

func TestAppServerShellRejectedAndEmpty(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "!\r")
	if w.Len() != 0 || u.draft != "!" || u.notice == "" {
		t.Fatal("empty shell command was submitted or silently discarded")
	}
	appServerTestKeys(t, u, "pwd\rnext")
	w.Reset()
	appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"unavailable"}}`)
	if u.draft != "next" || !u.noticeAlert || u.starting || u.shellStandalone || w.Len() != 0 || len(u.inputHistory) != 1 || u.inputHistory[0].text != "!pwd" {
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
	if !u.shellStandalone || w.Len() != 0 {
		t.Fatal("observed standalone shell misclassified")
	}
	appServerTestTurnEnd(t, u, "shell", "completed")
	appServerOneRequest(t, w, "turn/start", "follow up")
}

func TestAppServerShellOlderCompletionKeepsNewOrigin(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	w := u.client.Input.(*appServerTestInput)
	appServerTestTurn(t, u, "model")
	appServerTestKeys(t, u, "!pwd\r")
	appServerTestMessage(t, u, `{"id":1,"result":{}}`)
	appServerTestMessage(t, u, `{"method":"item/started","params":{"threadId":"main","turnId":"model","item":{"id":"a","type":"commandExecution","source":"userShell","command":"pwd","status":"inProgress"}}}`)
	appServerTestKeys(t, u, "!date\r")
	appServerTestMessage(t, u, `{"id":2,"result":{}}`)
	w.Reset()
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"model","item":{"id":"a","type":"commandExecution","source":"userShell","command":"pwd","status":"completed"}}}`)
	if u.shellOrigin == nil {
		t.Fatal("older completion settled newer shell submission")
	}
	appServerTestTurnEnd(t, u, "model", "completed")
	appServerTestTurn(t, u, "shell")
	appServerTestKeys(t, u, "follow up\r")
	if w.Len() != 0 || !u.shellStandalone {
		t.Fatal("follow-up steered standalone shell")
	}
	appServerTestTurnEnd(t, u, "shell", "completed")
	appServerOneRequest(t, w, "turn/start", "follow up")
}

func TestAppServerShellComposer(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "!echo $HOME")
	if u.completionTarget().kind != 0 || w.Len() != 0 {
		t.Fatal("shell variable opened a picker")
	}
	frame, _ := u.mainFrame(80, 20, 0)
	text := strings.Join(frame, "\n")
	if !strings.Contains(ansi.Strip(text), "Shell Mode") || !strings.Contains(text, activityui.Red+"╭") || !strings.Contains(text, activityui.Red+"│") {
		t.Fatalf("shell notice/frame missing: %q", text)
	}
	u.deleteDraftRange(0, 1)
	if u.shellMode() {
		t.Fatal("removing bang did not leave shell mode")
	}
}
