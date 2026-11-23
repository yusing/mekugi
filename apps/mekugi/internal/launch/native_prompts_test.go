package launch

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func nativeTestAnswers(t *testing.T, u *appServerUI, input sessionapi.NativeAnswers, preparations ...*sessionapi.PreparedInput) string {
	t.Helper()
	var prepared *sessionapi.PreparedInput
	if len(preparations) > 0 {
		prepared = preparations[0]
	}
	refusal, err := u.nativeSession.applyNativeAnswers(&u.attached, &u.picker, &u.Editor, u.side, u.clock, u.statusPanel != nil || u.resumePicker != nil, u.ctx, input, prepared, t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	return refusal
}

func nativeTestAnswerBatch(call prompt.CallView) sessionapi.NativeAnswers {
	input := sessionapi.NativeAnswers{Call: call.ID}
	for _, q := range call.Questions {
		if q.Pending {
			input.Answers = append(input.Answers, sessionapi.NativeQuestionAnswer{Call: call.ID, Question: q.ID})
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
	for _, kind := range []string{"sudoPassword", "gpgPassphrase"} {
		t.Run(kind, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.startSession("main", "/workspace")
			u.Turn = "turn"
			questionTestMessage(t, u, "secret-1", "shellGuard/requestSecret", map[string]any{
				"threadId": "main", "turnId": "turn", "startedAtMs": 1, "kind": kind, "cwd": "/workspace", "executable": "/usr/bin/sudo", "argv": []string{"sudo", "id"},
			})
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn", "status": "completed"}})
			wire.Reset()
			if len(nativePendingQuestions(u.attached.conversation)) != 1 {
				t.Fatal("turn completion withdrew a live shell process's request")
			}
			call := nativePendingQuestions(u.attached.conversation)[0]
			input := nativeTestAnswerBatch(call)
			input.Answers[0].Secret = "fixture\nsecret"
			if refusal := nativeTestAnswers(t, u, input); !strings.Contains(refusal, "single line") {
				t.Fatal("password validator did not reject multiline secret")
			}
			if wire.Len() != 0 || u.questions.Calls[0].Sent {
				t.Fatal("invalid password answered authentication")
			}
			input.Answers[0].Secret = "fixture-private-secret"
			if refusal := nativeTestAnswers(t, u, input); refusal != "" {
				t.Fatal(refusal)
			}
			if got := wire.String(); !strings.Contains(got, `"id":"secret-1"`) || !strings.Contains(got, `"decision":"provide"`) || !strings.Contains(got, `"secret":"fixture-private-secret"`) {
				t.Fatalf("native secret response missing: %s", got)
			}
			for _, value := range []any{u.attached.conversation, apiPrompts(&u.attached, &u.Client), nativePendingQuestions(u.attached.conversation), u.attached.presentation.View()} {
				data, err := json.Marshal(value, jsonv1.FormatDurationAsNano(true))
				if err != nil || strings.Contains(string(data), "fixture-private-secret") {
					t.Fatal("secret escaped into publication or retained state", err)
				}
			}
			if len(u.questions.secretState(call.ID).Answers) != 0 || len(u.questions.secretState(call.ID).Editors) != 0 {
				t.Fatal("server retained transient secret answers")
			}
		})
	}
}

func TestNativeSyncAnswersKeepNotesAndOtherEditor(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	questionTestSync(t, u, "first", false)
	first := nativePendingQuestions(u.attached.conversation)[0]
	u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.Editor, u.Thread)
	questionTestSync(t, u, "second", false)
	u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.Editor, u.Thread)
	openQuestionCall(&u.questions, &u.approvals, u.attached.conversation, &u.picker, &u.Editor, u.Thread, u.questions.calls()[1])
	u.Text = "unrelated editor"
	input := nativeTestAnswerBatch(first)
	input.Answers[0].Draft = composer.Draft{Text: "explicit note"}
	if refusal := nativeTestAnswers(t, u, input); refusal != "" {
		t.Fatal(refusal)
	}
	if !strings.Contains(wire.String(), `"choice":1,"note":"explicit note"`) || u.Text != "unrelated editor" {
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
	if !strings.Contains(wire.String(), `"id":"side-question"`) || !strings.Contains(wire.String(), `"other":"side answer"`) {
		t.Fatalf("native side answer did not retain original response authority: %s", wire.String())
	}
}
