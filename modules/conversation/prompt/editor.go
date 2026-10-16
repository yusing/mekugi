package prompt

import "github.com/yusing/mekugi/modules/conversation/composer"

// Editor parks Main's editor while a question or denial reason owns input.
// The renderer keeps geometry and its preferred cursor column separately.
type Editor struct{ Parked composer.State }

func (e *Editor) Open(editor *composer.Editor, answer composer.State) {
	e.Parked = editor.Park()
	editor.Resume(answer)
}
func (e *Editor) Close(editor *composer.Editor) composer.State {
	answer := editor.Park()
	editor.Resume(e.Parked)
	e.Parked = composer.State{}
	return answer
}
