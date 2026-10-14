package router

import (
	"bytes"
	json "encoding/json/v2"
	"path/filepath"
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

func TestAppServerTerminalTitleOrchestrationPrompts(t *testing.T) {
	u, source := orchestratePromptUI(t)
	second := *u.orchestrateThreads["child"]
	second.batch.TaskName = "second"
	launch := *second.batch.Launch
	launch.ThreadID = "child2"
	second.batch.Launch = &launch
	u.orchestrateThreads["child2"] = &second
	raw := mustMarshalJSON(map[string]any{"thread": appServerThreadInfo{ID: "child2", Cwd: second.batch.Cwd}})
	if err := u.addOrchestratedView(&second, "child2", raw, func() {}); err != nil {
		t.Fatal(err)
	}
	drainOrchestrateWork(t, u)
	u.navigation.views["child2"].awaitingTurn = false
	out := notificationTestOutput(t, u)
	for _, v := range u.promptViews() {
		v.notifications = u.notifications
	}
	check := func(action bool) {
		t.Helper()
		for _, thread := range []string{"main", "child2"} {
			u.switchOrchestratedThread(thread)
			viewed := u.viewedUI()
			u.clearTerminalTitle()
			out.Reset()
			viewed.writeTerminalTitle(time.Unix(0, 0))
			want := "Mekugi"
			if action {
				want += " [ ! ] Action Required"
			}
			want += " " + filepath.Base(viewed.session.cwd)
			if got := out.String(); got != "\x1b]0;"+want+"\a" {
				t.Fatalf("viewing %s: title = %q, want %q", thread, got, want)
			}
		}
	}
	orchestratePromptQuestion(t, u, false)
	check(true)
	source.resolveQuestionCall(source.questions.calls[0], "answered")
	check(false)
	var turns []appServerHistoryTurn
	if err := json.Unmarshal([]byte(`[{"id":"past","status":"completed","items":[{"id":"restored","type":"agentMessage","delivery":"async","questions":[{"title":"Which scope?","options":["Narrow"]}]}]}]`), &turns); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	source.restoreHistory(turns)
	if out.Len() != 0 {
		t.Fatalf("restoration emitted notification %q", out.String())
	}
	check(true)
	source.resolveQuestionCall(source.questions.calls[len(source.questions.calls)-1], "answered")
	check(false)
	approvalTestCommand(t, u, "batch-approval", map[string]any{"threadId": "child", "turnId": "batch-turn", "command": "echo batch"})
	check(true)
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "child", "requestId": "batch-approval"})
	check(false)
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

func TestAppServerTerminalTitleQueuedQuestionAnswerIsNotActionRequired(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/work/project")
	out := notificationTestOutput(t, u)
	u.turn = "turn"
	now := time.Unix(0, 0)
	questionTestAsync(t, u, "async", "Which option?")
	out.Reset()
	u.writeTerminalTitle(now)
	if got := out.String(); got != "\x1b]0;Mekugi [ ! ] Action Required project\a" {
		t.Fatalf("unanswered title = %q", got)
	}
	u.openQuestions()
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "1\r")
	if u.questionCount() != 0 || u.questions.calls[0].resolved || !u.questions.calls[0].sent {
		t.Fatal("queued answer was marked resolved or still actionable")
	}
	if rows := u.questionRows(80, 10); len(rows) != 0 {
		t.Fatalf("answered question still has a dock: %q", rows)
	}
	if preview := strings.Join(u.pendingInputPreview(80), "\n"); !strings.Contains(preview, "Answering 1 question(s)") {
		t.Fatalf("uncommitted answer has no pending preview: %q", preview)
	}
	out.Reset()
	u.writeTerminalTitle(now)
	if got := out.String(); got != "\x1b]0;Mekugi ⠋ project\a" {
		t.Fatalf("queued answer title = %q", got)
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
