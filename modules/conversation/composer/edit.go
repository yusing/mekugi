package composer

import (
	"errors"
	"unicode/utf8"
)

// Edit replaces a source range as one undoable operation. Bound tokens and
// Unicode graphemes remain atomic; screen coordinates never enter the editor.
func (e *Editor) Edit(start, end int, text string) error {
	if !e.validRange(start, end) || !utf8.ValidString(text) {
		return errors.New("Draft edit must use complete text or attachment boundaries.")
	}
	if start == end && text == "" {
		e.Run = NoRun
		return nil
	}
	e.Record()
	e.Draft.Remove(start, end)
	e.Draft.InsertText(text)
	e.Run = NoRun
	e.Changed()
	return nil
}

func (e *Editor) validRange(start, end int) bool {
	boundary := func(at int) bool {
		return at == 0 || at == len(e.Text) || e.Boundary(e.Boundary(at, -1), 1) == at
	}
	return start >= 0 && end >= start && end <= len(e.Text) && boundary(start) && boundary(end)
}

type ActionKind uint8

const (
	NoAction ActionKind = iota
	InsertAction
	DeleteAction
	MoveAction
	UndoAction
	RedoAction
	RecallPreviousAction
	RecallNextAction
)

// Action uses source coordinates. Key bindings and wrapped rows belong to the renderer.
type Action struct {
	Kind       ActionKind
	Start, End int
	Text       string
	Run        Run
}

// Apply preserves the editor's typed-word and deletion grouping.
func (e *Editor) Apply(action Action) (bool, error) {
	before := e.Revision()
	switch action.Kind {
	case NoAction:
	case InsertAction:
		e.Insert(action.Text)
	case DeleteAction:
		if !e.validRange(action.Start, action.End) || action.Run != NoRun && action.Run != BackspaceRun && action.Run != DeleteRun {
			return false, errors.New("Draft deletion must use complete text or attachment boundaries.")
		}
		e.Delete(action.Start, action.End, action.Run)
	case MoveAction:
		if !e.validRange(action.Start, action.Start) {
			return false, errors.New("Draft caret must use complete text or attachment boundaries.")
		}
		e.Run = NoRun
		if e.Cursor() != action.Start {
			e.CursorBack = len(e.Text) - action.Start
			e.Changed()
		}
	case UndoAction, RedoAction:
		e.Undo(action.Kind == RedoAction)
	case RecallPreviousAction, RecallNextAction:
		e.Run = NoRun
		e.Recall(action.Kind == RecallPreviousAction)
	default:
		return false, errors.New("Unknown draft action.")
	}
	return e.Revision() != before, nil
}
