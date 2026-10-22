package launch

import (
	"context"
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// Sent calls remain in the inventory until authoritative resolution so the
// originating client can restore its own editor if async delivery is rejected.
func nativePendingQuestions(conversation *conversation.Session) []prompt.CallView {
	var pending []prompt.CallView
	for _, call := range conversation.Questions.Calls {
		if call.Resolved {
			continue
		}
		view := call.View()
		for i := range view.Questions {
			if view.Questions[i].Pending {
				view.Questions[i].Selected, view.Questions[i].Note = 0, false
			}
		}
		pending = append(pending, view)
	}
	return pending
}

func nativePendingApprovals(conversation *conversation.Session) []prompt.ApprovalView {
	var pending []prompt.ApprovalView
	for _, approval := range conversation.Approvals.Pending {
		view := approval.View()
		view.Selected = 0
		pending = append(pending, view)
	}
	return pending
}

// prepareNativeAnswers validates a complete client-owned answer batch before any
// authoritative question changes. The trial is an admission value, not a store.
func prepareNativeAnswers(questions *nativeQuestionDock, control *sessioncontrol.Client[appServerRequest], input sessionapi.NativeAnswers, remoteHuman bool) (*sessioncontrol.Question, *prompt.Call, map[int][]string, string) {
	var handle *sessioncontrol.Question
	for _, call := range questions.Calls {
		if call.ID == input.Call && !call.Sent && !call.Resolved && (call.Local || call.Thread == control.Thread || control.Requests.WorkSide(call.Thread)) {
			handle = questions.handles[call]
			break
		}
	}
	if handle == nil || handle.Sudo != nil && handle.Sudo.Finished() {
		return nil, nil, nil, prompt.ErrQuestionUnavailable.Error()
	}
	if public := input.PublicAnswer(); public != nil {
		answers, incomplete, refusal := prepareCallAnswers(handle.Call, *public)
		if refusal != "" {
			return nil, nil, nil, refusal
		}
		input.Incomplete = incomplete
		for _, answer := range answers {
			input.Answers = append(input.Answers, sessionapi.NativeQuestionAnswer{AnswerInput: answer})
		}
	}
	trial := *handle.Call
	trial.Questions = slices.Clone(handle.Questions)
	model := prompt.Questions{Calls: []*prompt.Call{&trial}}
	view := handle.View()
	pending := 0
	for _, question := range view.Questions {
		if question.Pending {
			pending++
		}
	}
	if len(input.Answers) != pending {
		return nil, nil, nil, "Answer every question or skip it."
	}
	seen := make(map[string]bool, pending)
	secrets := make(map[int][]string)
	for _, answer := range input.Answers {
		index := slices.IndexFunc(view.Questions, func(q prompt.QuestionView) bool { return q.ID == answer.Question })
		if answer.Call != input.Call || index < 0 || !view.Questions[index].Pending || seen[answer.Question] {
			return nil, nil, nil, prompt.ErrQuestionUnavailable.Error()
		}
		seen[answer.Question] = true
		question := &trial.Questions[index]
		if remoteHuman && question.IsSecret {
			return nil, nil, nil, "Answer secret prompts in the terminal."
		}
		if !question.IsSecret {
			if answer.Secret != "" {
				return nil, nil, nil, "Secret input does not match this question."
			}
			if _, err := model.Answer(answer.AnswerInput); err != nil {
				return nil, nil, nil, err.Error()
			}
			continue
		}
		// The secret is never assigned to Question.Editor or Question.Answer.
		if answer.Choice < 0 || answer.Choice > len(question.Choices) || answer.Draft.Text != "" {
			return nil, nil, nil, "Secret answers require the secret input."
		}
		completed := *question
		completed.Selected = answer.Choice
		if !completed.Complete(answer.Secret, trial.Sync, answer.Skip) {
			return nil, nil, nil, prompt.ErrEmptyAnswer.Error()
		}
		question.Selected, question.Done, question.Skipped = answer.Choice, completed.Done, completed.Skipped
		question.Answer, question.Editor = nil, composer.State{}
		secrets[index] = completed.Answer
	}
	if _, err := model.Submission(input.Call, input.Incomplete); err != nil {
		return nil, nil, nil, err.Error()
	}
	if handle.Sudo != nil {
		candidate := *handle
		candidate.Call = &trial
		if err := candidate.ValidatePassword(secrets); err != nil {
			return nil, nil, nil, err.Error()
		}
	}
	return handle, &trial, secrets, ""
}

// applyNativeAnswers runs on the sole execution loop after per-view admission.
// Only accepted ordinary answer values enter the authoritative Call. Secret
// answers are borrowed solely for the existing response/password authority and
// discarded before the next event or publication.
func applyNativeAnswers(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, environment []string, owned map[string]bool, hook *approvalHookCheck, parent context.Context, observations *sessioncontrol.Observations, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, input sessionapi.NativeAnswers, prepared *sessionapi.PreparedInput, requestContext context.Context, remoteHuman bool) (string, error) {
	handle, trial, secrets, refusal := prepareNativeAnswers(questions, control, input, remoteHuman)
	if refusal != "" {
		return refusal, nil
	}
	if requestContext != nil && requestContext.Err() != nil || prepared != nil && !prepared.Admit(requestContext, owned) {
		return "The native answer was canceled before admission.", nil
	}
	if questions.Active == handle.Call {
		*editor = hideQuestionEditor(questions, attached.conversation, terminal, *editor, control.Thread)
	}
	previous := handle.Questions
	handle.Questions = trial.Questions
	secretEditors := make(map[int]composer.State)
	view := trial.View()
	for _, answer := range input.Answers {
		index := slices.IndexFunc(view.Questions, func(q prompt.QuestionView) bool { return q.ID == answer.Question })
		if index < 0 || !trial.Questions[index].IsSecret {
			continue
		}
		draft := answer.Draft.Clone()
		draft.Text = answer.Secret
		secretEditors[index] = composer.State{Snapshot: composer.Undo{Draft: draft}}
	}
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay)
	}()
	err := submitQuestionAnswers(attached, terminal, control, picker, editor, settings, tiers, restoring, side, clock, questions, approvals, overlay, environment, owned, hook, parent, observations, reviewOwner, receipts, handle, secretEditors, secrets)
	if !handle.Sent && !handle.Resolved {
		handle.Questions = previous
		if err == nil {
			return attached.feedback.Text, nil
		}
	}
	return "", err
}
