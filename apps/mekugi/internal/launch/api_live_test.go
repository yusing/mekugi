// Source: apps/mekugi/internal/launch/web_live_test.go:1:67@[14a6d1f3]
package launch

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func apiTestIntent(t *testing.T, u *appServerUI, intent sessionapi.Intent) string {
	t.Helper()
	refusal, err := applyAPIIntent(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations, u.ctx, u.review, &u.pendingEditReceipts, u.notifications, intent)
	if err != nil {
		t.Fatal(err)
	}
	return refusal
}

// Browser input steers Main's turn and Stop interrupts it, as Enter and Ctrl-C
// do, while the terminal displays a side conversation; the session status
// still describes Main.
func TestAPIIntentsActOnMain(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.Turn = "main-turn"
	oneoffTestStart(t, u, wire)

	if refusal := apiTestIntent(t, u, sessionapi.Intent{Action: sessionapi.Send, Text: " /status"}); refusal == "" || wire.Len() != 0 {
		t.Fatalf("command input refusal = %q, wire = %s", refusal, wire)
	}
	u.attached.conversation.Approvals.Open = true
	if refusal := apiTestIntent(t, u, sessionapi.Intent{Action: sessionapi.Send, Text: "go on"}); refusal != "Answer the open prompt first." || wire.Len() != 0 {
		t.Fatalf("input under an open prompt refusal = %q, wire = %s", refusal, wire)
	}
	u.attached.conversation.Approvals.Open = false

	if refusal := apiTestIntent(t, u, sessionapi.Intent{Action: sessionapi.Send, Text: "also check the docs"}); refusal != "" {
		t.Fatalf("input refused: %q", refusal)
	}
	steer := appServerOneRequest(t, wire, "turn/steer", "also check the docs")
	if steer.Params.ThreadID != "main" || steer.Params.ExpectedTurnID != "main-turn" {
		t.Fatalf("steer = %+v", steer.Params)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"main-turn"}}`, steer.ID))

	view := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	if !u.attached.presentation.DisplayingSide() || !view.Status.Session.Running || !sessionpresentation.MainInterruptible(&u.Live, u.attached.conversation) {
		t.Fatalf("Main's status while the side conversation shows = %+v", view.Status.Session)
	}
	if refusal := apiTestIntent(t, u, sessionapi.Intent{Action: sessionapi.Stop}); refusal != "" {
		t.Fatalf("stop refused: %q", refusal)
	}
	if r := appServerOneRequest(t, wire, "turn/interrupt", ""); r.Params.ThreadID != "main" || u.Interruption.Target != "main-turn" {
		t.Fatalf("interrupt = %+v, target %q", r.Params, u.Interruption.Target)
	}
}

func TestAPIIntentsRejectStaleAndCanceledAdmission(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, intent := range []sessionapi.Intent{
		{Action: sessionapi.Send, Text: "stale", Thread: "previous", Workspace: "/workspace"},
		{Action: sessionapi.Stop, Thread: "main", Workspace: "/previous"},
		{Action: sessionapi.Send, Text: "canceled", Context: ctx},
	} {
		if refusal := apiTestIntent(t, u, intent); refusal == "" || wire.Len() != 0 {
			t.Fatalf("inadmissible request reached host: refusal=%q wire=%s", refusal, wire)
		}
	}
}

// Source: apps/mekugi/internal/launch/web_shell_test.go:124:138@[14a6d1f3]
func TestAPIShellRejectionPreservesLocalDraft(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Text = "local prose"
	refusal, err := submitAPIShell(&u.attached, u.shell, &u.Client, &u.picker, u.clock, u.restoring != nil, u.skillEnvironment, u.ctx, u.review, &u.shellEdits, u.retained, u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane, "false")
	if err != nil || refusal != "" {
		t.Fatalf("shell: %q, %v", refusal, err)
	}
	if !strings.Contains(wire.String(), `"method":"thread/shellCommand"`) || u.Text != "local prose" {
		t.Fatalf("shell dispatch changed draft: %q, %s", u.Text, wire)
	}
	shellResponse(&u.attached, u.shell, &u.Client, &u.Editor, u.clock, &u.shellEdits, &appserver.Error{Message: "rejected"})
	if u.Text != "local prose" {
		t.Fatalf("remote rejection replaced local draft: %q", u.Text)
	}
	history := u.attached.conversation.Editor.InputHistory
	if len(history) != 1 || history[0].Text != "!false" {
		t.Fatalf("rejected command history: %+v", history)
	}
}
