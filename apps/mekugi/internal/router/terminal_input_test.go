package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
)

func (u *terminalUI) key(key byte) error {
	if event := u.Decoder.Feed(key, time.Now()); event.Kind != terminalsession.NoInput {
		return u.applyInput(u.frameInput().Dispatch(&u.Interaction, event))
	}
	return nil
}
func (u *terminalUI) send(key string) error {
	return u.applyInput(u.frameInput().Send(&u.Interaction, key))
}
func (u *terminalUI) mouse(report string) error {
	return u.applyInput(u.frameInput().Dispatch(&u.Interaction, terminalsession.Input{Kind: terminalsession.MouseInput, Text: report}))
}
func (u *terminalUI) flushEscape() error {
	if u.Decoder.Escape(time.Now()) {
		return u.send("\x1b")
	}
	return nil
}
func (u *terminalUI) selectNativePane(pane int) {
	if err := u.applyInput(u.frameInput().SelectPane(&u.Interaction, pane)); err != nil {
		panic(err)
	}
}
func (u *appServerUI) key(key string) (bool, error) {
	return u.applyMainKey(terminalsession.KeyEvent(key))
}
func (u *terminalUI) outputKey(key string) { u.outputIntent(u.Dialog.Key(key)) }
func (u *terminalUI) outputMouse(button, x, y int, release bool) {
	u.outputIntent(u.Dialog.Mouse(button, x, y, release))
}

func (u *terminalUI) selectionAction(action terminalsession.SelectionIntent) {
	if s := u.Selection; s == nil || s.Dragging() && action != 27 {
		return
	}
	if err := u.send(string(rune(action))); err != nil {
		panic(err)
	}
}

func (u *terminalUI) selectionMouse(button, x, y int, release bool) bool {
	if u.main == nil {
		return false
	}
	for {
		result := u.Frame.SelectionMouse(u.frameInput(), u.Selection, terminalsession.Mouse{Button: button, X: x, Y: y, Release: release})
		if len(result.Demands) > 0 {
			source := u.main.activitySource(result.Selection.View)
			if source == nil {
				u.main.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
				return true
			}
			result.Selection.View.Hydrate(source.ResolveOutputDemands(result.Demands))
			continue
		}
		u.Selection = result.Selection
		if result.Action != 0 {
			u.selectionAction(result.Action)
		}
		u.openSelectionTarget(result.Target)
		return result.Handled
	}
}
