package router

import (
	"net/url"
	"time"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func openSelectionTarget(attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, activityService *activitymodel.Service, files *detailFiles, surface *terminalsession.Attachment, target terminalsession.SelectionTarget) {
	if target.Link == "" && target.Entry == 0 && target.Snippet == (activityui.Snippet{}) {
		return
	}
	view := attached.presentation.View()
	if target.Version != view.Version || target.Thread != view.Displayed.ID || target.Workspace != view.Info.Workspace || target.Workspace != attached.cwd || activitySource(attached, terminal, target.View) == nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
		return
	}
	switch {
	case target.Link != "":
		if !files.openMarkdownFile(&attached.session, attached.presentation, attached.cwd, activitySource(attached, terminal, target.View), surface, target.View, target.Link) {
			link := target.Link
			if parsed, err := url.Parse(link); err == nil && parsed.Scheme == "file" {
				link = parsed.Path
			}
			copyText(&attached.feedback, terminal, clock, surface, link)
		}
	case target.Entry != 0:
		if target.View == terminal.Transcript && openActivityReply(attached, terminal, files, surface, target.Entry) {
			return
		}
		openEntry(attached, terminal, files, surface, target.View, target.Entry)
	default:
		snippet := target.Snippet
		if snippet.Block == activityui.EditNavigationSnippet {
			openActivityEdit(attached, terminal, files, activityService, surface, target.View, snippet.Run, snippet.Path)
		} else if !openOutput(attached, terminal, files, activityService, surface, target.View, snippet) {
			openEntry(attached, terminal, files, surface, target.View, snippet.Run)
		}
	}
}
