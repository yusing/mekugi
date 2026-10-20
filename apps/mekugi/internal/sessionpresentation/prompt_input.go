package sessionpresentation

import "github.com/yusing/mekugi/modules/conversation/prompt"

type QuestionInputIntent struct {
	Version Version
	prompt.QuestionInput
}

type ApprovalInputIntent struct {
	Version Version
	prompt.ApprovalInput
}

func (m *Model) QuestionInput(in QuestionInputIntent, questions *prompt.Questions) error {
	if in.Version != m.view.Version {
		return ErrStaleIntent
	}
	if m.replay {
		return ErrReplayIntent
	}
	if !questions.ApplyInput(in.QuestionInput) {
		return ErrStaleIntent
	}
	return nil
}

func (m *Model) ApprovalInput(in ApprovalInputIntent, approvals *prompt.Approvals) error {
	if in.Version != m.view.Version {
		return ErrStaleIntent
	}
	if m.replay {
		return ErrReplayIntent
	}
	if !approvals.ApplyInput(in.ApprovalInput) {
		return ErrStaleIntent
	}
	return nil
}
