package router

import (
	"net/url"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (main *appServerUI) openSelectionTarget(u *terminalsession.Attachment, target terminalsession.SelectionTarget) {
	if target.Link == "" && target.Entry == 0 && target.Snippet == (activityui.Snippet{}) {
		return
	}
	view := main.sessionPresentation().View()
	if target.Version != view.Version || target.Thread != view.Displayed.ID || target.Workspace != view.Info.Workspace || target.Workspace != main.cwd || main.activitySource(target.View) == nil {
		main.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
		return
	}
	switch {
	case target.Link != "":
		if !main.detailFiles.openMarkdownFile(&main.session, main.sessionPresentation(), main.cwd, main.activitySource(target.View), u, target.View, target.Link) {
			link := target.Link
			if parsed, err := url.Parse(link); err == nil && parsed.Scheme == "file" {
				link = parsed.Path
			}
			main.copyText(u, link)
		}
	case target.Entry != 0:
		if target.View == main.shell.Transcript && main.openActivityReply(u, target.Entry) {
			return
		}
		main.openEntry(u, target.View, target.Entry)
	default:
		snippet := target.Snippet
		if snippet.Block == activityui.EditNavigationSnippet {
			main.openActivityEdit(u, target.View, snippet.Run, snippet.Path)
		} else if !main.openOutput(u, target.View, snippet) {
			main.openEntry(u, target.View, snippet.Run)
		}
	}
}
