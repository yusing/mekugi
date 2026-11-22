package sessionpresentation

import "github.com/yusing/mekugi/modules/conversation/prompt"

type AnswerIntent struct {
	Version Version
	prompt.AnswerInput
}

type QuestionSubmitIntent struct {
	Version    Version
	Call       string
	Incomplete bool
	Questions  []string
}

func (m *Model) AnswerQuestion(intent AnswerIntent, questions *prompt.Questions) (*prompt.Question, error) {
	if m.admitOwner(intent.Version) != nil {
		return nil, ErrStaleIntent
	}
	if m.replay {
		return nil, ErrReplayIntent
	}
	return questions.Answer(intent.AnswerInput)
}

func (m *Model) SubmitQuestions(intent QuestionSubmitIntent, questions *prompt.Questions) (*prompt.Call, error) {
	if m.admitOwner(intent.Version) != nil {
		return nil, ErrStaleIntent
	}
	if m.replay {
		return nil, ErrReplayIntent
	}
	call, err := questions.Submission(intent.Call, intent.Incomplete)
	if err == nil {
		call.SubmitIDs = intent.Questions
	}
	return call, err
}
