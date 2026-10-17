package router

import (
	"testing"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestNonTerminalHistoryIntentsReachHostAndRetire(t *testing.T) {
	u, wire := newPagedHistoryUI(t)
	historyTurnsReply(t, u)
	restoreContentReply(t, u, 4, map[string]any{"data": []any{historyItem("last", "saved", 109000)}, "nextCursor": "older"})
	v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	if len(v.History.Entries) != 1 || v.History.Entries[0].Status != sessionpresentation.Unloaded || v.History.Target != "child" {
		t.Fatalf("history publication: %+v", v.History)
	}
	load := sessionpresentation.HistoryIntent{Version: v.Version, Thread: v.History.Target}
	u.testActivity(u.shell.Agents).displayThread("/root/worker")
	if err := u.loadOlderActivity(load); err != nil {
		t.Fatal(err)
	}
	if len(restoreContentRequests(t, wire)) != 5 || u.attached.feedback.Text != sessionpresentation.ErrStaleIntent.Error() {
		t.Fatal("stale history intent issued a request or hid its rejection")
	}
	if err := u.loadOlderActivity(historyLoadIntent(u)); err != nil {
		t.Fatal(err)
	}
	r := restoreContentRequests(t, wire)[5]
	if r.Method != "thread/items/list" || r.Params["threadId"] != "child" || r.Params["cursor"].(map[string]any)["itemId"] != "last" {
		t.Fatalf("semantic history request: %+v", r)
	}
	cancel := historyCancelIntent(u)
	u.testActivity(u.shell.Agents).displayThread("/root")
	if err := u.cancelOlderActivity(cancel); err != nil {
		t.Fatal(err)
	}
	if historyLoading(&u.attached, &u.childHistoryState) == nil || historyLoading(&u.attached, &u.childHistoryState).state.RequestID() == "" {
		t.Fatal("stale cancellation retired an observation")
	}
	if err := u.cancelOlderActivity(historyCancelIntent(u)); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 5, map[string]any{"data": []any{historyItem("late", "retired", 101000)}})
	if historyLoading(&u.attached, &u.childHistoryState) != nil || len(entryTexts(u.testActivity(u.shell.Agents))) != 1 || len(restoreContentRequests(t, wire)) != 6 {
		t.Fatal("cancelled response revived history or issued host work")
	}
	u.attached.presentation.Attach(sessionpresentation.Thread{ID: u.Thread, Caller: "/root"}, true)
	v = publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	if v.History.Target != "" {
		t.Fatal("replay offered a host history target")
	}
	if err := u.loadOlderActivity(sessionpresentation.HistoryIntent{Version: v.Version, Thread: "child"}); err != nil {
		t.Fatal(err)
	}
	if u.attached.feedback.Text != sessionpresentation.ErrReplayIntent.Error() || len(restoreContentRequests(t, wire)) != 6 {
		t.Fatal("replay history intent reached the host")
	}
}

func TestNonTerminalInterruptionIdentityAndDisposition(t *testing.T) {
	for _, mode := range []sessioncontrol.InterruptMode{sessioncontrol.RestoreWaiting, sessioncontrol.ExpediteSteers} {
		u, wire := newAppServerTestUI()
		appServerTestTurn(t, u, "old")
		v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
		intent := sessionpresentation.InterruptIntent{Version: v.Version, Target: v.Interrupt, Mode: mode}
		appServerTestTurn(t, u, "current")
		if err := interruptSession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.review, &u.pendingEditReceipts, intent); err != nil {
			t.Fatal(err)
		}
		if wire.Len() != 0 || u.Interruption.Target != "" || u.attached.feedback.Text != sessionpresentation.ErrStaleIntent.Error() {
			t.Fatal("retired turn intent interrupted newer work")
		}
		u.attached.conversation.Unsent = []conversationmodel.Input{{Text: "waiting"}}
		v = publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
		intent.Version, intent.Target = v.Version, v.Interrupt
		if err := interruptSession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.review, &u.pendingEditReceipts, intent); err != nil {
			t.Fatal(err)
		}
		r := appServerOneRequest(t, wire, "turn/interrupt", "")
		if r.Params.ThreadID != "main" || u.Interruption.Target != "current" || u.SendSteersAfterInterrupt != (mode == sessioncontrol.ExpediteSteers) {
			t.Fatal("interruption changed target or disposition")
		}
		if err := interruptSession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.review, &u.pendingEditReceipts, intent); err != nil || wire.Len() != 0 {
			t.Fatal("repeated intent repeated the interrupt")
		}
	}
}

func TestNonTerminalInterruptionPendingStartAndReplay(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Submit("first", "")
	v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	intent := sessionpresentation.InterruptIntent{Version: v.Version, Target: v.Interrupt}
	u.SubmissionFailed()
	u.Submit("second", "")
	if err := interruptSession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.review, &u.pendingEditReceipts, intent); err != nil || u.Interruption.BeforeStart {
		t.Fatal("retired start intent cancelled its replacement")
	}
	v = publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	intent.Version, intent.Target = v.Version, v.Interrupt
	if err := interruptSession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.review, &u.pendingEditReceipts, intent); err != nil || !u.Interruption.BeforeStart || wire.Len() != 0 {
		t.Fatal("pending start did not defer to its host turn")
	}
	appServerTestTurn(t, u, "started")
	appServerOneRequest(t, wire, "turn/interrupt", "")
	u.Interruption.Finish()
	u.attached.presentation.Attach(sessionpresentation.Thread{ID: u.Thread, Caller: "/root"}, true)
	v = publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	intent.Version, intent.Target = v.Version, v.Interrupt
	if err := interruptSession(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, &u.side, u.statusPanel != nil || u.resumePicker != nil, u.review, &u.pendingEditReceipts, intent); err != nil || wire.Len() != 0 || u.Interruption.Target != "" || u.attached.feedback.Text != sessionpresentation.ErrReplayIntent.Error() {
		t.Fatal("replay interruption reached the host")
	}
}
