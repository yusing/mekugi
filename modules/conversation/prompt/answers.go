package prompt

import (
	"errors"
	"strconv"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

var (
	ErrQuestionUnavailable = errors.New("The question is no longer available; select it again.")
	ErrSecretAnswer        = errors.New("Secret answers require the secret input.")
	ErrEmptyAnswer         = errors.New("Choose an option, enter an answer or skip the question.")
	ErrIncompleteAnswers   = errors.New("Confirm submission with unanswered questions.")
)

type AnswerInput struct {
	Call, Question string
	Choice         int
	Draft          composer.Draft
	Skip           bool
}

func (c *Call) questionID(index int) string { return c.ID + "/" + strconv.Itoa(index) }

func (m *Questions) pending(id string) *Call {
	for _, c := range m.Calls {
		if c.ID == id && !c.Resolved && !c.Sent {
			return c
		}
	}
	return nil
}

// Answer records explicit ordinary input, independent of the open editor.
// Source: apps/mekugi/internal/router/app_server_questions.go:321:337@ed87e571.
func (m *Questions) Answer(in AnswerInput) (*Question, error) {
	c := m.pending(in.Call)
	if c == nil {
		return nil, ErrQuestionUnavailable
	}
	for i := range c.Questions {
		q := &c.Questions[i]
		if c.questionID(i) != in.Question {
			continue
		}
		if q.Outcome != "" || in.Choice < 0 || in.Choice > len(q.Choices) {
			return nil, ErrQuestionUnavailable
		}
		if q.IsSecret {
			return nil, ErrSecretAnswer
		}
		if !in.Skip && in.Choice == len(q.Choices) && in.Draft.Text == "" {
			return nil, ErrEmptyAnswer
		}
		q.Selected = in.Choice
		q.Editor.Snapshot.Draft = in.Draft.Clone()
		q.Complete(in.Draft.Text, c.Sync, in.Skip)
		return q, nil
	}
	return nil, ErrQuestionUnavailable
}

func (m *Questions) Submission(id string, incomplete bool) (*Call, error) {
	c := m.pending(id)
	if c == nil {
		return nil, ErrQuestionUnavailable
	}
	if c.Gaps() > 0 && !incomplete {
		return nil, ErrIncompleteAnswers
	}
	return c, nil
}
