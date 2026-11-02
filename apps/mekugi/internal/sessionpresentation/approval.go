package sessionpresentation

import (
	"errors"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

type ApprovalIntent struct {
	Version Version
	Request string
	Choice  int
	Reason  string
}

func (m *Model) Approval(intent ApprovalIntent, approvals *prompt.Approvals) (*prompt.Approval, prompt.Choice, error) {
	if m.admitOwner(intent.Version) != nil {
		return nil, prompt.Choice{}, ErrStaleIntent
	}
	if m.replay {
		return nil, prompt.Choice{}, ErrReplayIntent
	}
	a, choice, ok := approvals.Decision(intent.Request, intent.Choice)
	if !ok {
		return nil, prompt.Choice{}, ErrStaleIntent
	}
	if intent.Reason != "" && !choice.Deny {
		return nil, prompt.Choice{}, errors.New("A reason can only accompany a denial.")
	}
	return a, choice, nil
}
