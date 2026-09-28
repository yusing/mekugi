package router

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestAppServerTerminalTitleLifecycle(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/work/project")
	out := notificationTestOutput(t, u)
	// Disabling desktop popups must not disable lifecycle state reporting.
	u.notifications.settings.Events = []byte("false")
	now := time.Unix(0, 0)
	title := func(want string) {
		t.Helper()
		out.Reset()
		u.writeTerminalTitle(now)
		if got := out.String(); got != "\x1b]0;"+want+"\a" {
			t.Fatalf("title = %q, want %q", got, want)
		}
		out.Reset()
		u.writeTerminalTitle(now)
		if out.Len() != 0 {
			t.Fatal("unchanged title was repeated")
		}
	}
	title("Mekugi project")
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"turn"}}}`)
	title("Mekugi ⠋ project")
	now = now.Add(100 * time.Millisecond)
	title("Mekugi ⠙ project")
	questionTestSync(t, u, "sync", false)
	title("Mekugi [ ! ] Action Required project")
	appServerTestMessage(t, u, `{"method":"serverRequest/resolved","params":{"threadId":"main","requestId":"sync"}}`)
	title("Mekugi ⠙ project")
	questionTestAsync(t, u, "async", "Which option?")
	title("Mekugi [ ! ] Action Required project")
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"turn","status":"completed"}}}`)
	out.Reset()
	u.writeTerminalTitle(now)
	if out.Len() != 0 || u.notifications.lastTitle != "Mekugi [ ! ] Action Required project" {
		t.Fatal("completion cleared a pending async question")
	}
	u.resolveQuestionCall(u.questions.calls[len(u.questions.calls)-1], "answered")
	title("Mekugi project")
	out.Reset()
	u.clearTerminalTitle()
	if out.String() != "\x1b]0;\a" {
		t.Fatalf("cleanup = %q", out.String())
	}
	out.Reset()
	u.clearTerminalTitle()
	if out.Len() != 0 {
		t.Fatal("cleanup repeated")
	}
	// Returning from the external editor must republish the title.
	title("Mekugi project")
}

func TestAppServerTerminalTitleUnsupportedRequests(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/work/project")
	out := notificationTestOutput(t, u)
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"turn"}}}`)
	for range 2 {
		appServerTestMessage(t, u, `{"id":42,"method":"item/commandExecution/requestApproval","params":{"threadId":"main","turnId":"turn"}}`)
	}
	if out.String() != "\x1b]9;Approval requested\a" {
		t.Fatalf("duplicate approval notifications: %q", out.String())
	}
	u.writeTerminalTitle(time.Unix(0, 0))
	if !strings.Contains(out.String(), "Action Required") {
		t.Fatal("approval did not mark blocked")
	}
	appServerTestMessage(t, u, `{"method":"serverRequest/resolved","params":{"threadId":"child","requestId":42}}`)
	if len(u.notifications.blocked) != 1 {
		t.Fatal("another thread resolved the request")
	}
	appServerTestMessage(t, u, `{"method":"serverRequest/resolved","params":{"threadId":"main","requestId":42}}`)
	out.Reset()
	u.writeTerminalTitle(time.Unix(0, 0))
	if out.String() != "\x1b]0;Mekugi ⠋ project\a" {
		t.Fatalf("resolved request title = %q", out.String())
	}
	appServerTestMessage(t, u, `{"id":43,"method":"item/fileChange/requestApproval","params":{"threadId":"main","turnId":"turn"}}`)
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"turn","status":"interrupted"}}}`)
	out.Reset()
	u.writeTerminalTitle(time.Unix(0, 0))
	if out.String() != "\x1b]0;Mekugi project\a" || len(u.notifications.blocked) != 0 {
		t.Fatalf("interrupted request title = %q", out.String())
	}
}

func TestAppServerTerminalTitleIsolationAndSanitizing(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/work/project\a\x1b[31m\n")
	out := notificationTestOutput(t, u)
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"child-turn"}}}`)
	u.writeTerminalTitle(time.Unix(0, 0))
	if out.String() != "\x1b]0;Mekugi project[31m\a" {
		t.Fatalf("unsafe title or child changed Main state: %q", out.String())
	}
	other, _ := newAppServerTestUI()
	other.notifications = &nativeNotifications{out: new(bytes.Buffer)}
	if other.notifications.lastTitle != "" || other.notifications.focused {
		t.Fatal("terminal state shared between clients")
	}
}
