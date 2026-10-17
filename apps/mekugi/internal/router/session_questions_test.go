package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func TestNonTerminalQuestionSubmissionKeepsOtherEditor(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Turn = "turn"
	questionTestSync(t, u, "first", false)
	v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	call := v.Questions.Call
	answer := sessionpresentation.AnswerIntent{Version: v.Version, AnswerInput: prompt.AnswerInput{
		Call: call.ID, Question: call.Questions[0].ID, Choice: 0, Draft: composer.Draft{Text: "explicit note"},
	}}
	if !u.answerQuestion(answer) {
		t.Fatal(u.attached.feedback.Text)
	}
	u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.shell, u.Editor, u.Thread)
	questionTestSync(t, u, "second", false)
	u.Editor = hideQuestionEditor(&u.questions, u.attached.conversation, u.shell, u.Editor, u.Thread)
	openQuestionCall(&u.questions, &u.approvals, u.attached.conversation, u.shell, &u.picker, &u.Editor, u.Thread, u.questions.calls()[1])
	u.Text = "another answer draft"
	submit := sessionpresentation.QuestionSubmitIntent{Version: publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock)).Version, Call: call.ID}
	if err := u.submitQuestions(submit); err != nil {
		t.Fatal(err)
	}
	if got := wire.String(); !strings.Contains(got, `"id":"first"`) || !strings.Contains(got, `"answers":["Narrow","user_note: explicit note"]`) {
		t.Fatalf("wrong response identity or explicit input: %s", got)
	}
	if u.questions.activeCall() == nil || u.questions.activeCall().ID == call.ID || u.Text != "another answer draft" {
		t.Fatal("submission replaced another prompt's editor")
	}
	wire.Reset()
	if u.answerQuestion(answer) || u.attached.feedback.Text != sessionpresentation.ErrStaleIntent.Error() {
		t.Fatal("answered call accepted another answer")
	}
	if err := u.submitQuestions(submit); err != nil || wire.Len() != 0 {
		t.Fatal("answered call responded again")
	}
	v = publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	submit = sessionpresentation.QuestionSubmitIntent{Version: v.Version, Call: v.Questions.Call.ID}
	if err := u.submitQuestions(submit); err != nil || wire.Len() != 0 || u.attached.feedback.Text != prompt.ErrIncompleteAnswers.Error() {
		t.Fatal("incomplete submission skipped confirmation")
	}
	submit.Incomplete = true
	submit.Version = u.attached.presentation.View().Version
	if err := u.submitQuestions(submit); err != nil || !strings.Contains(wire.String(), `"id":"second","result":{"answers":{}}`) {
		t.Fatal("confirmed incomplete call lost the host's empty-answer semantics")
	}
}

func TestNonTerminalQuestionBatchUsesOrdinarySubmission(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.Turn = "turn"
	questionTestAsync(t, u, "batch", "Who receives it?", "Who approves it?")
	v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	call := v.Questions.Call
	for i, q := range call.Questions {
		v = u.attached.presentation.View()
		if !u.answerQuestion(sessionpresentation.AnswerIntent{Version: v.Version, AnswerInput: prompt.AnswerInput{Call: call.ID, Question: q.ID, Choice: i}}) {
			t.Fatal(u.attached.feedback.Text)
		}
	}
	v = u.attached.presentation.View()
	if err := u.submitQuestions(sessionpresentation.QuestionSubmitIntent{Version: v.Version, Call: call.ID}); err != nil {
		t.Fatal(err)
	}
	requests := appServerTurnRequests(t, wire)
	if len(requests) != 1 || requests[0].Method != "turn/steer" || requests[0].Params.ExpectedTurnID != "turn" {
		t.Fatalf("question batch bypassed submission control: %+v", requests)
	}
	text := requests[0].text()
	if strings.Count(text, binding.QuestionReplyStart) != 1 || !strings.Contains(text, "Customers") || !strings.Contains(text, "Internal") {
		t.Fatalf("question batch lost answers or identity: %s", text)
	}
}

func TestNonTerminalQuestionRejectsSecretReplayAndRetiredView(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Turn = "turn"
	questionTestSync(t, u, "secret", true)
	v := publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock))
	answer := sessionpresentation.AnswerIntent{Version: v.Version, AnswerInput: prompt.AnswerInput{
		Call: v.Questions.Call.ID, Question: v.Questions.Call.Questions[0].ID,
		Choice: 0, Draft: composer.Draft{Text: "ordinary input"},
	}}
	if u.answerQuestion(answer) || u.attached.feedback.Text != prompt.ErrSecretAnswer.Error() || u.questions.Current().Done || u.questions.Current().Editor.Snapshot.Text != "" {
		t.Fatal("ordinary intent entered secret state")
	}
	u.attached.presentation.Attach(sessionpresentation.Thread{ID: u.Thread, Caller: "/root"}, true)
	answer.Version = publishPresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, presentationTime(u.clock)).Version
	if u.answerQuestion(answer) || u.attached.feedback.Text != sessionpresentation.ErrReplayIntent.Error() {
		t.Fatal("replay accepted an answer")
	}
	u.Thread = "replacement"
	attachPresentation(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread, &u.review, &u.commits, u.ctx, u.retained, u.changeSource, u.activity != nil)
	if u.answerQuestion(answer) || u.attached.feedback.Text != sessionpresentation.ErrStaleIntent.Error() || wire.Len() != 0 {
		t.Fatal("retired view answered a replacement prompt")
	}
}
