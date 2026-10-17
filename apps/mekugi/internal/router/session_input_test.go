package router

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

func TestNonTerminalCurrentPublicationWithoutPaint(t *testing.T) {
	u, wire := newAppServerTestUI()
	read := func() sessionpresentation.View { return u.attached.presentation.View() }
	edit := func(text string) {
		t.Helper()
		v := read()
		if !editInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, sessionpresentation.DraftEditIntent{Version: v.Version, Revision: v.Draft.Revision, End: len(v.Draft.Draft.Text), Text: text}) {
			t.Fatal(u.attached.feedback.Text)
		}
		if next := read(); next.Draft.Draft.Text != text || next.Version == v.Version {
			t.Fatalf("edit did not publish: %+v", next)
		}
	}
	edit("first")
	edit("second")
	v := read()
	for range 2 {
		if !reflect.DeepEqual(v, read()) {
			t.Fatal("reading refreshed the publication")
		}
	}
	if err := u.submitInput(sessionpresentation.SubmitIntent{Version: v.Version, Revision: v.Draft.Revision}); err != nil {
		t.Fatal(err)
	}
	r := appServerOneRequest(t, wire, "turn/start", "second")
	if read().Draft.Draft.Text != "" || read().Version == v.Version {
		t.Fatal("submission did not publish without a renderer")
	}
	edit("newer draft")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, r.ID))
	if got := read().Draft.Draft.Text; got != "second\nnewer draft" {
		t.Fatalf("host rejection was not published: %q", got)
	}
	edit("")
	appServerTestTurn(t, u, "turn")
	if read().Status.State.Text != "Working" || read().Interrupt.Turn != "turn" {
		t.Fatal("host turn did not publish")
	}
	questionTestSync(t, u, "prompt", false)
	if read().Questions.Call == nil || u.shell.Prompts.Ready() {
		t.Fatal("prompt was not published independently of its display receipt")
	}
	appServerTestKeys(t, u, "ordinary typing")
	if read().Draft.Draft.Text != "ordinary typing" || wire.Len() != 0 {
		t.Fatal("an unseen prompt consumed input intended for the ordinary draft")
	}
}

func editOrdinaryInput(t *testing.T, u *appServerUI, text string) {
	t.Helper()
	v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	if !v.Draft.Available || !editInput(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, sessionpresentation.DraftEditIntent{Version: v.Version, Revision: v.Draft.Revision, End: len(v.Draft.Draft.Text), Text: text}) {
		t.Fatalf("ordinary edit rejected: %s", u.attached.feedback.Text)
	}
}

func ordinarySubmitIntent(u *appServerUI, mode sessioncontrol.InputMode) sessionpresentation.SubmitIntent {
	v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	return sessionpresentation.SubmitIntent{Version: v.Version, Revision: v.Draft.Revision, Mode: mode}
}

func TestNonTerminalInputDispositionAndHostRequests(t *testing.T) {
	for _, tc := range []struct {
		name, turn, method string
		mode               sessioncontrol.InputMode
		blocked, question  bool
		queued, unsent     int
	}{
		{name: "idle send", method: "turn/start"},
		{name: "idle queue sends", mode: sessioncontrol.QueueNextTurn, method: "turn/start"},
		{name: "active steer", turn: "active", method: "turn/steer"},
		{name: "next turn", turn: "active", mode: sessioncontrol.QueueNextTurn, queued: 1},
		{name: "blocked send stacks", blocked: true, unsent: 1},
		{name: "hidden question queues", turn: "active", question: true, queued: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.Turn, u.Settings.RestoreEffort = tc.turn, tc.blocked
			if tc.question {
				questionTestSync(t, u, "waiting", false)
				u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.shell, u.Editor, u.Thread)
			}
			editOrdinaryInput(t, u, "ordinary input")
			intent := ordinarySubmitIntent(u, tc.mode)
			if err := u.submitInput(intent); err != nil {
				t.Fatal(err)
			}
			if tc.method != "" {
				r := appServerOneRequest(t, wire, tc.method, "ordinary input")
				if r.Params.ThreadID != "main" || r.Params.ExpectedTurnID != tc.turn {
					t.Fatalf("wrong submission target: %+v", r)
				}
			}
			v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
			if wire.Len() != 0 || v.Draft.Draft.Text != "" || len(u.attached.conversation.Queued) != tc.queued || len(u.attached.conversation.Unsent) != tc.unsent {
				t.Fatalf("wrong disposition: draft=%q queued=%d unsent=%d wire=%s", v.Draft.Draft.Text, len(u.attached.conversation.Queued), len(u.attached.conversation.Unsent), wire.String())
			}
			if err := u.submitInput(intent); err != nil || wire.Len() != 0 {
				t.Fatal("duplicate intent submitted again")
			}
		})
	}
}

func TestNonTerminalInputRevisionAndRejection(t *testing.T) {
	u, wire := newAppServerTestUI()
	editOrdinaryInput(t, u, "same text")
	stale := ordinarySubmitIntent(u, sessioncontrol.SendInput)
	editOrdinaryInput(t, u, "new draft")
	editOrdinaryInput(t, u, "same text")
	if err := u.submitInput(stale); err != nil || wire.Len() != 0 || publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock)).Draft.Draft.Text != "same text" {
		t.Fatal("identical replacement revived a retired draft target")
	}
	if err := u.submitInput(ordinarySubmitIntent(u, sessioncontrol.SendInput)); err != nil {
		t.Fatal(err)
	}
	r := appServerOneRequest(t, wire, "turn/start", "same text")
	editOrdinaryInput(t, u, "newer typing")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, r.ID))
	if got := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock)).Draft.Draft.Text; got != "same text\nnewer typing" {
		t.Fatalf("rejection lost ordering: %q", got)
	}
}

func TestNonTerminalOrdinaryInputRejectsPromptAndReadOnlyViews(t *testing.T) {
	u, wire := newAppServerTestUI()
	editOrdinaryInput(t, u, "keep draft")
	intent := ordinarySubmitIntent(u, sessioncontrol.SendInput)
	questionTestSync(t, u, "secret", true)
	openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
	if v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock)); v.Draft.Available || v.Draft.Draft.Text != "" {
		t.Fatal("prompt editor leaked into ordinary publication")
	}
	if err := u.submitInput(intent); err != nil || wire.Len() != 0 || u.questions.Current() == nil {
		t.Fatal("ordinary submit took a prompt editor")
	}
	u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.shell, u.Editor, u.Thread)
	v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	if err := u.attached.presentation.Display(sessionpresentation.DisplayIntent{Version: v.Version, Thread: sessionpresentation.Thread{ID: "child", Caller: "/root/child"}}); err != nil {
		t.Fatal(err)
	}
	if err := u.submitInput(ordinarySubmitIntent(u, sessioncontrol.SendInput)); err != nil || wire.Len() != 0 || publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock)).Draft.Draft.Text != "keep draft" {
		t.Fatal("child display submitted Main input")
	}
	u.attached.presentation.Attach(sessionpresentation.Thread{ID: u.Thread, Caller: "/root"}, true)
	if err := u.submitInput(ordinarySubmitIntent(u, sessioncontrol.SendInput)); err != nil || wire.Len() != 0 {
		t.Fatal("replay submitted input")
	}
}

func TestDraftDeletionSettlesNewSecretEditorBeforeReturning(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Text = "kept draft"
	questionTestSync(t, u, "pending-secret", true)
	if u.questions.Current() != nil {
		t.Fatal("pending question captured a nonempty draft")
	}
	before := u.attached.presentation.View().Version
	deleteDraftRange(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState,
		preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier},
		u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock,
		&u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, 0, len(u.Text))
	view := u.attached.presentation.View()
	if u.Editor != &u.shell.Prompts.Secret || u.questions.Current() == nil {
		t.Fatal("deletion returned before the pending secret editor opened")
	}
	if view.Version == before || view.Questions.Call == nil || view.Questions.Call.ID != u.questions.activeCall().ID {
		t.Fatal("nested settlement did not publish the newly active prompt")
	}
	if u.shell.Prompts.Ready() {
		t.Fatal("publication acknowledged an unpainted prompt")
	}
}
