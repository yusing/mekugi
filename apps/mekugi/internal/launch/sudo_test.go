package launch

import (
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffrender"
)

func TestUISnapshotSudoDialogs(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	u.attached.presentation.Status.Text, u.settings.Model, u.settings.Effort = "Ready", "snapshot-model", "high"
	u.startSession("main", "/workspace")
	u.Turn = "turn"
	questionTestMessage(t, u, "sudo-1", "shellGuard/requestApproval", map[string]any{
		"threadId": "main", "turnId": "turn", "startedAtMs": 1, "kind": "sudo", "cwd": "/workspace", "executable": "/usr/bin/sudo", "argv": []string{"sudo", "apt", "update"},
	})
	rows, _ := u.testMainFrame(80, 20, 0)
	assertNativeUISnapshot(t, "native-sudo-approval", rows)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "1\r")
	if !strings.Contains(wire.String(), `"id":"sudo-1"`) || !strings.Contains(wire.String(), `"decision":"accept"`) {
		t.Fatalf("approval response = %s", wire.String())
	}
	questionTestMessage(t, u, "sudo-pw-1", "shellGuard/requestSecret", map[string]any{
		"threadId": "main", "turnId": "turn", "startedAtMs": 2, "kind": "sudoPassword", "cwd": "/workspace", "executable": "/usr/bin/sudo", "argv": []string{"sudo", "apt", "update"},
	})
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "1234-secret")
	rows, _ = u.testMainFrame(80, 20, 0)
	assertNativeUISnapshot(t, "native-sudo-password", rows)
}

func TestSudoPasswordWithdrawal(t *testing.T) {
	u, wire := newAppServerTestUI()
	questionTestMessage(t, u, "sudo-pw-2", "shellGuard/requestSecret", map[string]any{
		"threadId": "main", "turnId": "turn", "startedAtMs": 1, "kind": "sudoPassword", "cwd": "/workspace", "executable": "/usr/bin/sudo", "argv": []string{"sudo", "id"},
	})
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "private-draft")
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "sudo-pw-2"})
	if u.questions.activeCall() != nil || len(u.InputHistory) != 0 || len(u.questions.calls()) != 0 {
		t.Fatal("withdrawal retained a secret draft")
	}
	if wire.Len() != 0 {
		t.Fatalf("answered a withdrawn sudo request: %s", wire.String())
	}
}

func TestSudoSecretEditorAndSessionSwitch(t *testing.T) {
	u, input := newAppServerTestUI()
	questionTestMessage(t, u, "sudo-pw-3", "shellGuard/requestSecret", map[string]any{
		"threadId": "main", "turnId": "old-turn", "startedAtMs": 1, "kind": "sudoPassword", "cwd": "/workspace", "executable": "/usr/bin/sudo", "argv": []string{"sudo", "id"},
	})
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "fixture-secret")
	neutral, err := json.Marshal(u.attached.conversation)
	if err != nil || strings.Contains(string(neutral), "fixture-secret") || u.Editor == &u.attached.conversation.Editor {
		t.Fatal("secret editor escaped into Conversation state", err)
	}
	questions := u.questions.Questions
	call := u.questions.activeCall().Call
	if questions != &u.attached.conversation.Questions || call.Questions[0].Editor.Snapshot.Text != "" || len(call.Questions[0].Answer) != 0 {
		t.Fatal("secret entered neutral question state")
	}
	if _, err := u.key("\x07"); err != nil || u.Text != "fixture-secret" {
		t.Fatalf("secret editor dispatch: %v", err)
	}
	u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.Editor, u.Thread)
	if u.questions.secret.Text != "" || u.Editor != &u.attached.conversation.Editor {
		t.Fatal("hiding authentication did not return to the ordinary editor")
	}
	replaceAppServerTestSession(t, u, "replacement")
	if u.questions.Questions == questions || len(u.questions.Calls) != 1 || u.questions.Calls[0] != call {
		t.Fatal("local authentication did not retain identity in its replacement owner")
	}
	input.Reset()
	u.Thread, u.Turn = "new-thread", ""
	appServerTestKeys(t, u, "new task\r")
	if !strings.Contains(input.String(), "turn/start") {
		t.Fatal("old-session password blocked new-session input")
	}
	openQuestions(&u.attached, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
	questionTestPaint(t, u, 80)
	if u.Text != "fixture-secret" {
		t.Fatal("session switch lost the password draft")
	}
	input.Reset()
	appServerTestKeys(t, u, "\r")
	if got := input.String(); !strings.Contains(got, `"id":"sudo-pw-3"`) || !strings.Contains(got, `"decision":"provide"`) || !strings.Contains(got, `"secret":"fixture-secret"`) {
		t.Fatalf("password did not resolve after switch: %s", got)
	}
}

func TestInterruptedSecretRequestDeclinesWithoutDraft(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.Turn = "turn"
	questionTestMessage(t, u, "secret", "shellGuard/requestSecret", map[string]any{
		"threadId": "main", "turnId": "turn", "kind": "gpgPassphrase",
		"cwd": "/workspace", "executable": "/usr/bin/gpg", "argv": []string{"gpg", "--sign"},
	})
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "fixture-private")
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn", "status": "interrupted"}})
	got := wire.String()
	if !strings.Contains(got, `"id":"secret"`) || !strings.Contains(got, `"decision":"decline"`) || strings.Contains(got, "fixture-private") || len(u.InputHistory) != 0 || len(u.questions.calls()) != 0 {
		t.Fatal("interruption did not privately decline the outstanding secret request")
	}
}
