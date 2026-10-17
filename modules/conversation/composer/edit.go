package composer

import (
	"errors"
	"unicode/utf8"
)

// Edit replaces a source range as one undoable operation. Bound tokens and
// Unicode graphemes remain atomic; screen coordinates never enter the editor.
func (e *Editor) Edit(start, end int, text string) error {
	boundary := func(at int) bool {
		return at == 0 || at == len(e.Text) || e.Boundary(e.Boundary(at, -1), 1) == at
	}
	if start < 0 || end < start || end > len(e.Text) || !boundary(start) || !boundary(end) || !utf8.ValidString(text) {
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
