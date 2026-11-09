package launch

import (
	"fmt"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/contracts/activitydetail"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func (files *detailFiles) openActivityEdit(attached *attachedPresentation, source *sessionpresentation.ActivitySource, changes *changecapture.Service, seq uint64, path string) bool {
	pages, selected := activityEditPages(attached, source, changes, seq, path)
	for _, page := range pages {
		if page.ID != selected {
			continue
		}
		if page.Lang == "diff" && files.openDiff != nil {
			files.openDiff(page)
			return true
		}
		files.openDetailSelection(&attached.session, attached.presentation.View().Version, source, pages, false)
		files.selected = selected
		return true
	}
	return false
}

func activityEditPages(attached *attachedPresentation, source *sessionpresentation.ActivitySource, changes *changecapture.Service, seq uint64, path string) ([]activitypresentation.DetailPage, string) {
	for _, entry := range source.Entries {
		native := entry.Source.Native
		if entry.Source.Seq != seq || native == nil {
			continue
		}
		request := activitydetail.Request{Thread: native.Thread, Item: native.Item, Path: path}
		for _, value := range entry.Values {
			for _, original := range activitypresentation.Originals(value) {
				request.Directory = request.Directory || original.DirectoryDeletion() && original.EditPath() == path
			}
		}
		withPreparedSource := func(pages []activitypresentation.DetailPage) []activitypresentation.DetailPage {
			pages = activitypresentation.OrderEditDetails(pages, entry.Values)
			for i := range pages {
				pages[i].PreparedSource = seq
			}
			return pages
		}
		if changes != nil {
			result := changes.ActivityDetail(attached.session.Scope(), request)
			if len(result.Pages) > 0 {
				return withPreparedSource(activitypresentation.ContributedDetails(result.Pages)), result.Selected
			}
		}
		var pages []activitypresentation.DetailPage
		if receipt := native.CapturedEdit; receipt != nil && len(receipt.Pages) > 0 {
			pages = activitypresentation.ContributedDetails(receipt.Pages)
		} else {
			pages = activitypresentation.DetailPages(native.EditPages)
			for i := range pages {
				pages[i].ID = fmt.Sprintf("host:%s\x00%s/%d", native.Thread, native.Item, i)
			}
		}
		for _, page := range pages {
			if path == "" || page.Path == path || page.DisplayPath == path {
				return withPreparedSource(pages), page.ID
			}
		}
		// An observed shell edit can have no captured changes, for example a
		// temporary checkpoint. Its painted target still names a real operation.
		// Admit only a path in that operation, then show its original command
		// rather than treating missing diff evidence as a stale source identity.
		if len(pages) > 0 {
			return nil, ""
		}
		matched := false
		for _, value := range entry.Values {
			for _, original := range activitypresentation.Originals(value) {
				matched = matched || original.EditOperation() && original.EditPath() == path
			}
		}
		if !matched {
			return nil, ""
		}
		command := sessionpresentation.CommandPresentation(entry.Source)
		if command.Script == "" {
			return nil, ""
		}
		values := command.DetailValues()
		if len(values) == 0 {
			detail := activitypresentation.Value{Source: seq, Kind: "op", Verb: "Run", Code: command.Display, Lang: "bash", Fenced: true,
				Body: "No captured diff is available for this operation."}
			if command.Output != nil {
				detail.Output = 1
			}
			values = []activitypresentation.Value{detail}
		}
		pages = activitypresentation.DetailPages(values)
		for _, page := range pages {
			if page.EditOperation() && page.EditPath() == path {
				return pages, page.ID
			}
		}
		return pages, pages[0].ID
	}
	return nil, ""
}
