package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func TestNonTerminalQuestionSubmissionKeepsOtherEditor(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Turn = "turn"
	questionTestSync(t, u, "first", false)
	v := u.publishPresentation(u.now())
	call := v.Questions.Call
	answer := sessionpresentation.AnswerIntent{Version: v.Version, AnswerInput: prompt.AnswerInput{
		Call: call.ID, Question: call.Questions[0].ID, Choice: 0, Draft: composer.Draft{Text: "explicit note"},
	}}
	if !u.answerQuestion(answer) {
		t.Fatal(u.feedback.Text)
	}
	u.hideQuestions()
	questionTestSync(t, u, "second", false)
	u.hideQuestions()
	u.openQuestionCall(u.questionCalls()[1])
	u.Text = "another answer draft"
	submit := sessionpresentation.QuestionSubmitIntent{Version: u.publishPresentation(u.now()).Version, Call: call.ID}
	if err := u.submitQuestions(submit); err != nil {
		t.Fatal(err)
	}
	if got := wire.String(); !strings.Contains(got, `"id":"first"`) || !strings.Contains(got, `"answers":["Narrow","user_note: explicit note"]`) {
		t.Fatalf("wrong response identity or explicit input: %s", got)
	}
	if u.activeQuestionCall() == nil || u.activeQuestionCall().ID == call.ID || u.Text != "another answer draft" {
		t.Fatal("submission replaced another prompt's editor")
	}
	wire.Reset()
	if u.answerQuestion(answer) || u.feedback.Text != sessionpresentation.ErrStaleIntent.Error() {
		t.Fatal("answered call accepted another answer")
	}
	if err := u.submitQuestions(submit); err != nil || wire.Len() != 0 {
		t.Fatal("answered call responded again")
	}
	v = u.publishPresentation(u.now())
	submit = sessionpresentation.QuestionSubmitIntent{Version: v.Version, Call: v.Questions.Call.ID}
	if err := u.submitQuestions(submit); err != nil || wire.Len() != 0 || u.feedback.Text != prompt.ErrIncompleteAnswers.Error() {
		t.Fatal("incomplete submission skipped confirmation")
	}
	submit.Incomplete = true
	submit.Version = u.sessionPresentation().View().Version
	if err := u.submitQuestions(submit); err != nil || !strings.Contains(wire.String(), `"id":"second","result":{"answers":{}}`) {
		t.Fatal("confirmed incomplete call lost the host's empty-answer semantics")
	}
}

func TestNonTerminalQuestionBatchUsesOrdinarySubmission(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.Turn = "turn"
	questionTestAsync(t, u, "batch", "Who receives it?", "Who approves it?")
	v := u.publishPresentation(u.now())
	call := v.Questions.Call
	for i, q := range call.Questions {
		v = u.sessionPresentation().View()
		if !u.answerQuestion(sessionpresentation.AnswerIntent{Version: v.Version, AnswerInput: prompt.AnswerInput{Call: call.ID, Question: q.ID, Choice: i}}) {
			t.Fatal(u.feedback.Text)
		}
	}
	v = u.sessionPresentation().View()
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
	v := u.publishPresentation(u.now())
	answer := sessionpresentation.AnswerIntent{Version: v.Version, AnswerInput: prompt.AnswerInput{
		Call: v.Questions.Call.ID, Question: v.Questions.Call.Questions[0].ID,
		Choice: 0, Draft: composer.Draft{Text: "ordinary input"},
	}}
	if u.answerQuestion(answer) || u.feedback.Text != prompt.ErrSecretAnswer.Error() || u.currentQuestion().Done || u.currentQuestion().Editor.Snapshot.Text != "" {
		t.Fatal("ordinary intent entered secret state")
	}
	u.replay = &uiReplayPlayback{}
	u.attachPresentation()
	answer.Version = u.publishPresentation(u.now()).Version
	if u.answerQuestion(answer) || u.feedback.Text != sessionpresentation.ErrReplayIntent.Error() {
		t.Fatal("replay accepted an answer")
	}
	u.replay = nil
	u.Thread = "replacement"
	u.attachPresentation()
	if u.answerQuestion(answer) || u.feedback.Text != sessionpresentation.ErrStaleIntent.Error() || wire.Len() != 0 {
		t.Fatal("retired view answered a replacement prompt")
	}
}
