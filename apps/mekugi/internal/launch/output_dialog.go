package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// openBlocks is the shared modal entry point. It never changes pane navigation.
func openBlocks(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, u *terminalsession.Attachment, view *activityui.View, pages []activityui.Block) {
	openDetails(attached, terminal, files, u, view, activitypresentation.DetailPages(activityui.Values(pages)))
}

func openDetails(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, u *terminalsession.Attachment, view *activityui.View, pages []activitypresentation.DetailPage) {
	if len(pages) == 0 {
		return
	}
	files.openDetailSelection(&attached.session, attached.presentation.View().Version, activitySource(attached, terminal, view), u, view, pages, false)
}

func openEntry(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, u *terminalsession.Attachment, view *activityui.View, seq uint64) bool {
	pages := entryDetailPages(activitySource(attached, terminal, view), seq)
	openDetails(attached, terminal, files, u, view, pages)
	return len(pages) > 0
}

func entryDetailPages(source *sessionpresentation.ActivitySource, seq uint64) []activitypresentation.DetailPage {
	for index, entry := range source.Entries {
		if entry.Source.Seq != seq {
			continue
		}
		blocks := source.SourceValues(index)
		for i := range blocks {
			blocks[i].Source = entry.Source.Seq
			if blocks[i].Verb == "" {
				blocks[i].Verb = entry.Source.Agent
			}
		}
		return activitypresentation.DetailPages(blocks)
	}
	return nil
}
