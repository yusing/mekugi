package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func (files *detailFiles) openErrorDetails(attached *attachedPresentation, source *sessionpresentation.ActivitySource) {
	display := attached.presentation.View()
	var pages []activitypresentation.DetailPage
	for i, entry := range source.Entries {
		if entry.Source.Kind != "error" || entry.Publish().Hidden() {
			continue
		}
		if source.Audience != sessionpresentation.MainAudience && entry.Source.Agent == "/root" {
			continue
		}
		if display.Displayed.Caller != "" && display.Displayed.Caller != "/root" && entry.Source.Agent != display.Displayed.Caller {
			continue
		}
		values := source.SourceValues(i)
		for i := range values {
			values[i].Source = entry.Source.Seq
		}
		pages = append(pages, activitypresentation.DetailPages(values)...)
	}
	if len(pages) > 0 {
		files.openDetailSelection(&attached.session, display.Version, source, pages, false)
		files.selected = pages[len(pages)-1].ID
	}
}
