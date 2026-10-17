package prompt

// QuestionInput names the displayed question without retaining a model pointer.
// InputNone validates the target for navigation or a separate answer operation.
type QuestionInput struct {
	Call, Question string
	Kind           QuestionInputKind
	Choice         int
	Enabled        bool
}

type QuestionInputKind uint8

const (
	InputNone QuestionInputKind = iota
	InputChoose
	InputEdit
	InputNote
	InputCancelConfirm
)

func (m *Questions) ApplyInput(in QuestionInput) bool {
	c := m.Active
	if c == nil || c.ID != in.Call || c.questionID(m.Index) != in.Question || c.Resolved || c.Sent {
		return false
	}
	q := m.Current()
	switch in.Kind {
	case InputNone:
	case InputChoose:
		if in.Choice < 0 || in.Choice > len(q.Choices) {
			return false
		}
		q.Choose(in.Choice)
	case InputEdit:
		q.Edit(in.Enabled)
	case InputNote:
		if in.Enabled && (!c.Sync || q.Selected >= len(q.Choices)) {
			return false
		}
		q.Note = in.Enabled
	case InputCancelConfirm:
		m.Confirm = false
	default:
		return false
	}
	return true
}

type ApprovalInput struct {
	Request string
	Select  bool
	Choice  int
}

func (m *Approvals) ApplyInput(in ApprovalInput) bool {
	if !m.Open || len(m.Pending) == 0 || m.Pending[0].ID != in.Request {
		return false
	}
	a := m.Pending[0]
	if in.Select {
		for i, choice := range a.Choices {
			if choice.ID == in.Choice {
				a.Selected = i
				return true
			}
		}
		return false
	}
	return true
}
