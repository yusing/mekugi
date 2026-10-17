package router

import (
	"fmt"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

func editOrdinaryInput(t *testing.T, u *appServerUI, text string) {
	t.Helper()
	v := u.mainPresentation(u.now())
	if !v.Draft.Available || !u.editInput(sessionpresentation.DraftEditIntent{Version: v.Version, Revision: v.Draft.Revision, End: len(v.Draft.Draft.Text), Text: text}) {
		t.Fatalf("ordinary edit rejected: %s", u.feedback.Text)
	}
}

func ordinarySubmitIntent(u *appServerUI, mode sessioncontrol.InputMode) sessionpresentation.SubmitIntent {
	v := u.mainPresentation(u.now())
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
				u.hideQuestions()
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
			v := u.mainPresentation(u.now())
			if wire.Len() != 0 || v.Draft.Draft.Text != "" || len(u.conversation().Queued) != tc.queued || len(u.conversation().Unsent) != tc.unsent {
				t.Fatalf("wrong disposition: draft=%q queued=%d unsent=%d wire=%s", v.Draft.Draft.Text, len(u.conversation().Queued), len(u.conversation().Unsent), wire.String())
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
	if err := u.submitInput(stale); err != nil || wire.Len() != 0 || u.mainPresentation(u.now()).Draft.Draft.Text != "same text" {
		t.Fatal("identical replacement revived a retired draft target")
	}
	if err := u.submitInput(ordinarySubmitIntent(u, sessioncontrol.SendInput)); err != nil {
		t.Fatal(err)
	}
	r := appServerOneRequest(t, wire, "turn/start", "same text")
	editOrdinaryInput(t, u, "newer typing")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, r.ID))
	if got := u.mainPresentation(u.now()).Draft.Draft.Text; got != "same text\nnewer typing" {
		t.Fatalf("rejection lost ordering: %q", got)
	}
}

func TestNonTerminalOrdinaryInputRejectsPromptAndReadOnlyViews(t *testing.T) {
	u, wire := newAppServerTestUI()
	editOrdinaryInput(t, u, "keep draft")
	intent := ordinarySubmitIntent(u, sessioncontrol.SendInput)
	questionTestSync(t, u, "secret", true)
	u.openQuestions()
	if v := u.mainPresentation(u.now()); v.Draft.Available || v.Draft.Draft.Text != "" {
		t.Fatal("prompt editor leaked into ordinary publication")
	}
	if err := u.submitInput(intent); err != nil || wire.Len() != 0 || u.currentQuestion() == nil {
		t.Fatal("ordinary submit took a prompt editor")
	}
	u.hideQuestions()
	v := u.mainPresentation(u.now())
	if err := u.sessionPresentation().Display(sessionpresentation.DisplayIntent{Version: v.Version, Thread: sessionpresentation.Thread{ID: "child", Caller: "/root/child"}}); err != nil {
		t.Fatal(err)
	}
	if err := u.submitInput(ordinarySubmitIntent(u, sessioncontrol.SendInput)); err != nil || wire.Len() != 0 || u.mainPresentation(u.now()).Draft.Draft.Text != "keep draft" {
		t.Fatal("child display submitted Main input")
	}
	u.replay = &uiReplayPlayback{}
	if err := u.submitInput(ordinarySubmitIntent(u, sessioncontrol.SendInput)); err != nil || wire.Len() != 0 {
		t.Fatal("replay submitted input")
	}
}
