package launch

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func nativeTestAnswers(t *testing.T, u *appServerUI, input sessionapi.NativeAnswers, preparations ...*sessionapi.PreparedInput) string {
	t.Helper()
	var prepared *sessionapi.PreparedInput
	if len(preparations) > 0 {
		prepared = preparations[0]
	}
	refusal, err := applyNativeAnswers(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.skillEnvironment, u.ownedImages, &u.guardHookCheck, u.ctx, u.controlObservations, u.review, &u.pendingEditReceipts, input, prepared, t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	return refusal
}

func nativeTestAnswerBatch(call prompt.CallView) sessionapi.NativeAnswers {
	input := sessionapi.NativeAnswers{Call: call.ID}
	for _, q := range call.Questions {
		if q.Pending {
			input.Answers = append(input.Answers, sessionapi.NativeQuestionAnswer{AnswerInput: prompt.AnswerInput{Call: call.ID, Question: q.ID}})
		}
	}
	return input
}

func TestNativeAnswersAtomicAndConcurrentRefusal(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.Turn = "turn"
	questionTestAsync(t, u, "batch", "Who receives it?", "Who approves it?")
	call := nativePendingQuestions(u.attached.conversation)[0]
	input := nativeTestAnswerBatch(call)
	input.Answers[0].Draft.Text = "first client draft"
	input.Answers[1].Choice = 99
	before := slices.Clone(u.questions.Calls[0].Questions)
	prepared := sessionapi.NewPreparedInput(composer.Draft{}, nil)
	if refusal := nativeTestAnswers(t, u, input, prepared); refusal == "" || wire.Len() != 0 || prepared.Admitted() || !reflect.DeepEqual(before, u.questions.Calls[0].Questions) {
		t.Fatal("invalid batch changed authoritative answers")
	}
	input.Answers[1].Choice = 1
	if refusal := nativeTestAnswers(t, u, input, prepared); refusal != "" || !prepared.Admitted() {
		t.Fatal(refusal)
	}
	if len(appServerTurnRequests(t, wire)) != 1 {
		t.Fatal("final batch did not use the sole host ledger")
	}
	wire.Reset()
	if refusal := nativeTestAnswers(t, u, input); refusal == "" || wire.Len() != 0 {
		t.Fatal("concurrent client answered an already submitted call")
	}
}

func TestNativeSecretAnswerPrivacyAndPasswordValidation(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	request := &sessioncontrol.ShellApprovalRequest{Kind: "sudo-password", Thread: "main", Argv: []string{"sudo", "id"}, Reply: make(chan shellapproval.Reply, 1), Done: make(chan struct{})}
	openSudoPassword(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, false, u.notifications, &sessioncontrol.Approval{Approval: &prompt.Approval{Thread: "main", Subject: "sudo id"}, Guard: request})
	call := nativePendingQuestions(u.attached.conversation)[0]
	input := nativeTestAnswerBatch(call)
	input.Answers[0].Secret = "fixture\nsecret"
	if refusal := nativeTestAnswers(t, u, input); !strings.Contains(refusal, "single line") {
		t.Fatal("password validator did not reject multiline secret")
	}
	if len(request.Reply) != 0 || u.questions.Calls[0].Sent {
		t.Fatal("invalid password answered authentication")
	}
	input.Answers[0].Secret = "fixture-private-secret"
	if refusal := nativeTestAnswers(t, u, input); refusal != "" {
		t.Fatal(refusal)
	}
	reply := <-request.Reply
	if !reply.OK || reply.Password != input.Answers[0].Secret {
		t.Fatal("native secret did not reach original authentication authority")
	}
	if wire.Len() != 0 {
		t.Fatal("local authentication used host request transport")
	}
	for _, value := range []any{u.attached.conversation, apiPrompts(&u.attached, &u.Client), nativePendingQuestions(u.attached.conversation), u.attached.presentation.View()} {
		data, err := json.Marshal(value, jsonv1.FormatDurationAsNano(true))
		if err != nil || strings.Contains(string(data), "fixture-private-secret") {
			t.Fatal("secret escaped into publication or retained state", err)
		}
	}
	if len(u.shell.Prompts.SecretState(call.ID).Answers) != 0 || len(u.shell.Prompts.SecretState(call.ID).Editors) != 0 {
		t.Fatal("server retained transient secret answers")
	}
}

func TestNativeSyncAnswersKeepNotesAndOtherEditor(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	questionTestSync(t, u, "first", false)
	first := nativePendingQuestions(u.attached.conversation)[0]
	u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.shell, u.Editor, u.Thread)
	questionTestSync(t, u, "second", false)
	u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.shell, u.Editor, u.Thread)
	openQuestionCall(&u.questions, &u.approvals, u.attached.conversation, u.shell, &u.picker, &u.Editor, u.Thread, u.questions.calls()[1])
	u.Text = "unrelated editor"
	input := nativeTestAnswerBatch(first)
	input.Answers[0].Draft = composer.Draft{Text: "explicit note"}
	if refusal := nativeTestAnswers(t, u, input); refusal != "" {
		t.Fatal(refusal)
	}
	if !strings.Contains(wire.String(), `"answers":["Narrow","user_note: explicit note"]`) || u.Text != "unrelated editor" {
		t.Fatal("native submission lost note or replaced unrelated editor")
	}
}

func TestNativeAnswersUseOriginalSideRequest(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.Turn = "turn"
	oneoffTestStart(t, u, wire)
	questionTestMessage(t, u, "side-question", "item/tool/requestUserInput", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "itemId": "ask", "questions": []any{map[string]any{"id": "scope", "header": "Side", "question": "Side question?"}}})
	input := nativeTestAnswerBatch(nativePendingQuestions(u.attached.conversation)[0])
	input.Answers[0].Draft.Text = "side answer"
	wire.Reset()
	if refusal := nativeTestAnswers(t, u, input); refusal != "" {
		t.Fatal(refusal)
	}
	if !strings.Contains(wire.String(), `"id":"side-question"`) || !strings.Contains(wire.String(), `"answers":["user_note: side answer"]`) {
		t.Fatalf("native side answer did not retain original response authority: %s", wire.String())
	}
}
