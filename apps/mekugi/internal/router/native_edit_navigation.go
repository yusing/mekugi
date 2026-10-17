package router

import (
	"fmt"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/contracts/activitydetail"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// openActivityEdit resolves an exact admitted invocation without consulting the
// Diff painter, retained storage, or current workspace contents.
func (main *appServerUI) openActivityEdit(u *terminalsession.Attachment, view *activityui.View, seq uint64, path string) bool {
	pages, selected := main.activityEditPages(view, seq, path)
	for i, page := range pages {
		if page.ID == selected {
			main.openDetails(u, view, pages)
			u.Dialog.ShowPage(i)
			return true
		}
	}
	return false
}

func (main *appServerUI) activityEditPages(view *activityui.View, seq uint64, path string) ([]activitypresentation.DetailPage, string) {
	for _, entry := range main.activitySource(view).Entries {
		native := entry.Source.Native
		if entry.Source.Seq != seq || native == nil {
			continue
		}
		request := activitydetail.Request{Operation: activitydetail.Operation{Thread: native.Thread, Item: native.Item}, Path: path}
		for _, value := range entry.Values {
			for _, original := range activitypresentation.Originals(value) {
				request.Directory = request.Directory || original.DirectoryDeletion() && original.EditPath() == path
			}
		}
		if main != nil {
			result := main.activity.ActivityDetail(main.attached.session.Scope(), request)
			if len(result.Pages) > 0 {
				return activitypresentation.ContributedDetails(result.Pages), result.Selected
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
			if path == "" || page.Path == path {
				return pages, page.ID
			}
		}
		return nil, ""
	}
	return nil, ""
}
