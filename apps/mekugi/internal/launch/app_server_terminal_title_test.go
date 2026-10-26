package launch

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
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
		u.notifications.writeTerminalTitle(&u.attached.feedback, u.shell, u.clock, u.attached.cwd, u.Thread, u.questions.Questions, u.approvals.Approvals, u.Turn != "" || u.Starting(), now)
		if got := out.String(); got != "\x1b]0;"+want+"\a" {
			t.Fatalf("title = %q, want %q", got, want)
		}
		out.Reset()
		u.notifications.writeTerminalTitle(&u.attached.feedback, u.shell, u.clock, u.attached.cwd, u.Thread, u.questions.Questions, u.approvals.Approvals, u.Turn != "" || u.Starting(), now)
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
	u.notifications.writeTerminalTitle(&u.attached.feedback, u.shell, u.clock, u.attached.cwd, u.Thread, u.questions.Questions, u.approvals.Approvals, u.Turn != "" || u.Starting(), now)
	if out.Len() != 0 || u.notifications.lastTitle != "Mekugi [ ! ] Action Required project" {
		t.Fatal("completion cleared a pending async question")
	}
	resolveQuestionCall(&u.attached, u.shell, &u.Client, &u.Editor, &u.questions, &u.approvals, u.clock, u.review, &u.pendingEditReceipts, u.questions.calls()[len(u.questions.calls())-1], "answered")
	title("Mekugi project")
	out.Reset()
	u.notifications.clearTerminalTitle(&u.attached.feedback, u.shell, u.clock)
	if out.String() != "\x1b]0;\a" {
		t.Fatalf("cleanup = %q", out.String())
	}
	out.Reset()
	u.notifications.clearTerminalTitle(&u.attached.feedback, u.shell, u.clock)
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
	u.notifications.writeTerminalTitle(&u.attached.feedback, u.shell, u.clock, u.attached.cwd, u.Thread, u.questions.Questions, u.approvals.Approvals, u.Turn != "" || u.Starting(), time.Unix(0, 0))
	if !strings.Contains(out.String(), "Action Required") {
		t.Fatal("approval did not mark blocked")
	}
	appServerTestMessage(t, u, `{"method":"serverRequest/resolved","params":{"threadId":"child","requestId":42}}`)
	if len(u.notifications.blocked) != 1 {
		t.Fatal("another thread resolved the request")
	}
	appServerTestMessage(t, u, `{"method":"serverRequest/resolved","params":{"threadId":"main","requestId":42}}`)
	out.Reset()
	u.notifications.writeTerminalTitle(&u.attached.feedback, u.shell, u.clock, u.attached.cwd, u.Thread, u.questions.Questions, u.approvals.Approvals, u.Turn != "" || u.Starting(), time.Unix(0, 0))
	if out.String() != "\x1b]0;Mekugi ⠋ project\a" {
		t.Fatalf("resolved request title = %q", out.String())
	}
	appServerTestMessage(t, u, `{"id":43,"method":"item/fileChange/requestApproval","params":{"threadId":"main","turnId":"turn"}}`)
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"turn","status":"interrupted"}}}`)
	out.Reset()
	u.notifications.writeTerminalTitle(&u.attached.feedback, u.shell, u.clock, u.attached.cwd, u.Thread, u.questions.Questions, u.approvals.Approvals, u.Turn != "" || u.Starting(), time.Unix(0, 0))
	if out.String() != "\x1b]0;Mekugi project\a" || len(u.notifications.blocked) != 0 {
		t.Fatalf("interrupted request title = %q", out.String())
	}
}

func TestAppServerTerminalTitleQueuedQuestionAnswerIsNotActionRequired(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/work/project")
	out := notificationTestOutput(t, u)
	u.Turn = "turn"
	now := time.Unix(0, 0)
	questionTestAsync(t, u, "async", "Which option?")
	out.Reset()
	u.notifications.writeTerminalTitle(&u.attached.feedback, u.shell, u.clock, u.attached.cwd, u.Thread, u.questions.Questions, u.approvals.Approvals, u.Turn != "" || u.Starting(), now)
	if got := out.String(); got != "\x1b]0;Mekugi [ ! ] Action Required project\a" {
		t.Fatalf("unanswered title = %q", got)
	}
	openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "1\r")
	if u.questions.Count(u.Thread) != 0 || u.questions.calls()[0].Resolved || !u.questions.calls()[0].Sent {
		t.Fatal("queued answer was marked resolved or still actionable")
	}
	if rows := testQuestionRows(u, 80, 10); len(rows) != 0 {
		t.Fatalf("answered question still has a dock: %q", rows)
	}
	if preview := strings.Join(conversationui.Pending(publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side).Pending, 80), "\n"); !strings.Contains(preview, "Answering 1 question(s)") {
		t.Fatalf("uncommitted answer has no pending preview: %q", preview)
	}
	out.Reset()
	u.notifications.writeTerminalTitle(&u.attached.feedback, u.shell, u.clock, u.attached.cwd, u.Thread, u.questions.Questions, u.approvals.Approvals, u.Turn != "" || u.Starting(), now)
	if got := out.String(); got != "\x1b]0;Mekugi ⠋ project\a" {
		t.Fatalf("queued answer title = %q", got)
	}
}

func TestAppServerTerminalTitleIsolationAndSanitizing(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/work/project\a\x1b[31m\n")
	out := notificationTestOutput(t, u)
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"child-turn"}}}`)
	u.notifications.writeTerminalTitle(&u.attached.feedback, u.shell, u.clock, u.attached.cwd, u.Thread, u.questions.Questions, u.approvals.Approvals, u.Turn != "" || u.Starting(), time.Unix(0, 0))
	if out.String() != "\x1b]0;Mekugi project[31m\a" {
		t.Fatalf("unsafe title or child changed Main state: %q", out.String())
	}
	other, _ := newAppServerTestUI()
	other.notifications = &nativeNotifications{out: new(bytes.Buffer)}
	if other.notifications.lastTitle != "" || other.notifications.focused {
		t.Fatal("terminal state shared between clients")
	}
}

func TestTerminalTitleUnavailableOutputDoesNotReadPrompts(t *testing.T) {
	for _, notifications := range []*nativeNotifications{nil, {}} {
		notifications.writeTerminalTitle(nil, nil, nil, "", "", nil, nil, false, time.Time{})
	}
}
