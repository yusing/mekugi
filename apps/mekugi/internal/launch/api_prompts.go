// Source: apps/mekugi/internal/launch/web_prompts.go:1:110@[14a6d1f3]
package launch

import (
	"errors"
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func apiPrompts(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest]) *sessionapi.Prompts {
	p := &sessionapi.Prompts{}
	agent := func(thread string) string {
		if thread == control.Thread || thread == "" {
			return ""
		}
		return attached.session.KnownCaller(thread)
	}
	for _, call := range attached.conversation.Questions.Calls {
		if call.Sent || call.Resolved || (!call.Local && call.Thread != control.Thread) {
			continue
		}
		c := sessionapi.QuestionCall{ID: call.ID, Agent: agent(call.Thread)}
		for _, q := range call.View().Questions {
			if !q.Pending {
				continue
			}
			item := sessionapi.Question{ID: q.ID, Secret: q.Secret}
			if !q.Secret {
				item.Header, item.Text = textsanitize.Safe(q.Header, false), textsanitize.Safe(q.Text, false)
				for _, o := range q.Choices {
					item.Options = append(item.Options, sessionapi.Option{Label: textsanitize.Safe(o.Label, false), Description: textsanitize.Safe(o.Description, false)})
				}
			}
			c.Questions = append(c.Questions, item)
		}
		p.Questions = append(p.Questions, c)
	}
	for _, a := range attached.conversation.Approvals.Pending {
		item := sessionapi.Approval{ID: a.ID, Agent: agent(a.Thread), Title: textsanitize.Safe(a.Title, false), Subject: textsanitize.Safe(a.Subject, false)}
		for _, detail := range a.Details {
			item.Details = append(item.Details, textsanitize.Safe(detail, false))
		}
		for _, c := range a.Choices {
			item.Choices = append(item.Choices, sessionapi.Choice{ID: c.ID, Label: textsanitize.Safe(c.Label, false), Deny: c.Deny})
		}
		p.Approvals = append(p.Approvals, item)
	}
	return p
}

// Validate the whole submission with the question owner's rules before changing
// any live draft. A malformed later answer must not leave earlier answers behind.
func prepareAPIAnswers(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], input sessionapi.Answer) ([]prompt.AnswerInput, bool, string) {
	for _, call := range attached.conversation.Questions.Calls {
		if call.ID != input.Call || call.Sent || call.Resolved || (!call.Local && call.Thread != control.Thread) {
			continue
		}
		return prepareCallAnswers(call, input)
	}
	return nil, false, "This prompt was already answered."
}

// prepareCallAnswers converts public input using the actual pending question owner.
func prepareCallAnswers(call *prompt.Call, input sessionapi.Answer) ([]prompt.AnswerInput, bool, string) {
	view := call.View()
	pending := 0
	for _, q := range view.Questions {
		if q.Pending {
			pending++
		}
	}
	if len(input.Answers) != pending {
		return nil, false, "Answer every question or skip it."
	}
	copy := *call
	copy.Questions = slices.Clone(call.Questions)
	trial := prompt.Questions{Calls: []*prompt.Call{&copy}}
	seen := make(map[string]bool, len(input.Answers))
	answers := make([]prompt.AnswerInput, 0, len(input.Answers))
	incomplete := false
	for _, answer := range input.Answers {
		index := slices.IndexFunc(view.Questions, func(q prompt.QuestionView) bool { return q.ID == answer.Question })
		if index < 0 || !view.Questions[index].Pending || seen[answer.Question] {
			return nil, false, "Answer every question once."
		}
		seen[answer.Question] = true
		choice := len(call.Questions[index].Choices)
		if answer.Choice != nil {
			choice = *answer.Choice
		}
		draft := answer.Draft()
		if choice < len(call.Questions[index].Choices) || answer.Skip {
			if len(answer.Attachments) > 0 && !answer.Skip {
				return nil, false, "Attachments require a written answer."
			}
			draft = composer.Draft{}
		} else if strings.TrimSpace(draft.Text) == "" {
			draft.Text = ""
		}
		a := prompt.AnswerInput{Call: call.ID, Question: answer.Question, Choice: choice, Draft: draft, Skip: answer.Skip}
		if _, err := trial.Answer(a); err != nil {
			switch {
			case errors.Is(err, prompt.ErrSecretAnswer):
				return nil, false, "Answer secret prompts in the terminal."
			case errors.Is(err, prompt.ErrEmptyAnswer):
				return nil, false, "Choose an option, enter an answer or skip the question."
			default:
				return nil, false, "This question or option is no longer available."
			}
		}
		answers = append(answers, a)
		incomplete = incomplete || answer.Skip
	}
	return answers, incomplete, ""
}
