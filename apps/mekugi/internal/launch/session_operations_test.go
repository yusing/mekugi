package launch

import (
	"testing"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestNonTerminalInterruptionIdentityAndDisposition(t *testing.T) {
	for _, mode := range []sessioncontrol.InterruptMode{sessioncontrol.RestoreWaiting, sessioncontrol.ExpediteSteers} {
		u, wire := newAppServerTestUI()
		appServerTestTurn(t, u, "old")
		v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
		intent := sessionpresentation.InterruptIntent{Version: v.Version, Target: v.Interrupt, Mode: mode}
		appServerTestTurn(t, u, "current")
		if err := interruptSession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.review, &u.pendingEditReceipts, intent); err != nil {
			t.Fatal(err)
		}
		if wire.Len() != 0 || u.Interruption.Target != "" || u.attached.feedback.Text != sessionpresentation.ErrInterruptUnavailable.Error() {
			t.Fatal("retired turn intent interrupted newer work")
		}
		u.attached.conversation.Unsent = []conversationmodel.Input{{Text: "waiting"}}
		v = publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
		intent.Version, intent.Target = v.Version, v.Interrupt
		if err := interruptSession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.review, &u.pendingEditReceipts, intent); err != nil {
			t.Fatal(err)
		}
		r := appServerOneRequest(t, wire, "turn/interrupt", "")
		if r.Params.ThreadID != "main" || u.Interruption.Target != "current" || u.SendSteersAfterInterrupt != (mode == sessioncontrol.ExpediteSteers) {
			t.Fatal("interruption changed target or disposition")
		}
		if err := interruptSession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.review, &u.pendingEditReceipts, intent); err != nil || wire.Len() != 0 {
			t.Fatal("repeated intent repeated the interrupt")
		}
	}
}

func TestNonTerminalInterruptionPendingStartAndReplay(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Submit("first", "")
	v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	intent := sessionpresentation.InterruptIntent{Version: v.Version, Target: v.Interrupt}
	u.SubmissionFailed()
	u.Submit("second", "")
	if err := interruptSession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.review, &u.pendingEditReceipts, intent); err != nil || u.Interruption.BeforeStart {
		t.Fatal("retired start intent cancelled its replacement")
	}
	v = publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	intent.Version, intent.Target = v.Version, v.Interrupt
	if err := interruptSession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.review, &u.pendingEditReceipts, intent); err != nil || !u.Interruption.BeforeStart || wire.Len() != 0 {
		t.Fatal("pending start did not defer to its host turn")
	}
	appServerTestTurn(t, u, "started")
	appServerOneRequest(t, wire, "turn/interrupt", "")
	u.Interruption.Finish()
	u.attached.presentation.Attach(sessionpresentation.Thread{ID: u.Thread, Caller: "/root"}, true)
	v = publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	intent.Version, intent.Target = v.Version, v.Interrupt
	if err := interruptSession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.review, &u.pendingEditReceipts, intent); err != nil || wire.Len() != 0 || u.Interruption.Target != "" || u.attached.feedback.Text != sessionpresentation.ErrReplayIntent.Error() {
		t.Fatal("replay interruption reached the host")
	}
}
