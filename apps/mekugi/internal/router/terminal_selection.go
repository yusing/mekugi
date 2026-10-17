package router

import (
	"net/url"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

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
