package router

import (
	"strings"
	"testing"
)

func TestAppServerOrchestrateExitConfirmation(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"active"}}`)
	child := u.navigation.views["child"]
	u.draft = "Main draft"
	u.switchOrchestratedThread("child")
	child.draft = "/quit"
	appServerTestKeys(t, child, "\r")
	if !child.quitConfirmation || child.quitRequested || u.orchestrateClosing {
		t.Fatal("exit did not wait for confirmation")
	}
	// Escape declines through the terminal decoder, without interrupting a turn.
	child.shell.send("\x1b")
	if child.quitConfirmation || child.quitRequested || child.draft != "/quit" || u.draft != "Main draft" || !u.orchestrateBusy() || u.viewedUI() != child {
		t.Fatal("declining exit changed live work or drafts")
	}
	appServerTestKeys(t, child, "\r")
	// Pasted Enter cannot confirm; paste remains ordinary opaque draft input.
	appServerTestKeys(t, child, "\x1b[200~input\r\x1b[201~")
	if child.quitRequested || child.quitConfirmation || !strings.Contains(child.draft, "input") {
		t.Fatal("paste confirmed exit or was lost", child.draft)
	}
	child.draft = "/quit"
	appServerTestKeys(t, child, "\r")
	if err := child.shell.key('\r'); err != nil {
		t.Fatal(err)
	}
	if !child.quitRequested || !u.orchestrateClosing || child.draft != "" || u.draft != "Main draft" {
		t.Fatal("confirmation did not exit through the coordinator")
	}
}

func TestAppServerOrchestrateDescendantExitConfirmation(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"active"}}`)
	u.registerSessionThread(appServerThreadInfo{ID: "native", ParentThreadID: "child", Source: []byte(`{"subAgent":{"thread_spawn":{"agent_path":"/root/worker"}}}`)})
	orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"native","turn":{"id":"native-turn"}}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"active","status":"completed"}}}`)
	w := u.client.Input.(*appServerTestInput)
	w.Reset()
	appServerTestKeys(t, u, "/quit\r")
	if !u.quitConfirmation || w.Len() != 0 {
		t.Fatal("native work did not require confirmation")
	}
	if err := u.shell.key('\r'); err != nil {
		t.Fatal(err)
	}
	if !u.quitRequested || !u.orchestrateClosing || w.Len() != 0 {
		t.Fatal("exit dispatched or left native work outside shutdown")
	}
}

func TestAppServerOrchestratePromptCancelsExitConfirmation(t *testing.T) {
	u, _ := orchestratePromptUI(t)
	u.draft = "/quit"
	appServerTestKeys(t, u, "\r")
	orchestratePromptQuestion(t, u, true)
	u.openQuestions()
	u.mainFrame(80, 24, 0)
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "child", "requestId": "batch-question"})
	if u.quitConfirmation {
		t.Fatal("opening a prompt left exit confirmation active")
	}
	if err := u.shell.key('\r'); err != nil {
		t.Fatal(err)
	}
	if u.quitRequested || !u.quitConfirmation || u.orchestrateClosing {
		t.Fatal("resolved prompt revived a stale exit confirmation")
	}
}
