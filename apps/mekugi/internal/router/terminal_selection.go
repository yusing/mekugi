package router

import (
	"net/url"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// selectionKey owns scrolling and action dispatch for both selection surfaces.
// Callers retain navigation dismissal and their surrounding Escape priority.
func (u *terminalUI) selectionKey(key string) bool {
	s := u.selection
	if s == nil {
		return false
	}
	intent, handled := s.Key(key, false)
	if intent != terminalsession.SelectionScroll {
		u.selectionAction(intent)
	}
	return handled
}

func (u *terminalUI) selectionAction(action terminalsession.SelectionIntent) {
	s := u.selection
	if s == nil || s.Dragging() && action != 27 {
		return
	}
	switch action {
	case 'r':
		description, source := s.MentionDescription()
		if !u.main.referenceInput(u.main.mainView.Reference(description, s.Text(), source)) {
			return
		}
		u.Focus = 0
		u.closeOutput()
	case 'c':
		u.copyText(s.Text())
	case 27:
	default:
		return
	}
	u.selection = nil
}

func (u *terminalUI) selectionMouse(button, x, y int, release bool) bool {
	if u.main == nil {
		return false
	}
	for {
		result := u.frame.SelectionMouse(u.frameInput(), u.selection, terminalsession.Mouse{Button: button, X: x, Y: y, Release: release})
		if len(result.Demands) > 0 {
			source := u.main.activitySource(result.Selection.View)
			if source == nil {
				u.main.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
				return true
			}
			result.Selection.View.Hydrate(source.ResolveOutputDemands(result.Demands))
			continue
		}
		u.selection = result.Selection
		if result.Action != 0 {
			u.selectionAction(result.Action)
		}
		u.openSelectionTarget(result.Target)
		return result.Handled
	}
}

func (u *terminalUI) openSelectionTarget(target terminalsession.SelectionTarget) {
	if target.Link == "" && target.Entry == 0 && target.Snippet == (activityui.Snippet{}) {
		return
	}
	view := u.main.sessionPresentation().View()
	if target.Version != view.Version || target.Thread != view.Displayed.ID || target.Workspace != view.Info.Workspace || target.Workspace != u.main.cwd || u.main.activitySource(target.View) == nil {
		u.main.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
		return
	}
	switch {
	case target.Link != "":
		if !u.main.openMarkdownFile(u, target.View, target.Link) {
			link := target.Link
			if parsed, err := url.Parse(link); err == nil && parsed.Scheme == "file" {
				link = parsed.Path
			}
			u.copyText(link)
		}
	case target.Entry != 0:
		if target.View == u.main.view && u.openActivityReply(target.Entry) {
			return
		}
		u.openEntry(target.View, target.Entry)
	default:
		snippet := target.Snippet
		if snippet.Block == activityui.EditNavigationSnippet {
			u.openActivityEdit(target.View, snippet.Run, snippet.Path)
		} else if !u.openOutput(target.View, snippet) {
			u.openEntry(target.View, snippet.Run)
		}
	}
}
