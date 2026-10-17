package router

import (
	"fmt"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/contracts/activitydetail"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// openActivityEdit resolves an exact admitted invocation without consulting the
// Diff painter, retained storage, or current workspace contents.
func (u *terminalUI) openActivityEdit(view *activityui.View, seq uint64, path string) bool {
	pages, selected := u.activityEditPages(view, seq, path)
	for i, page := range pages {
		if page.ID == selected {
			u.openDetails(view, pages)
			u.output.ShowPage(i)
			return true
		}
	}
	return false
}

func (u *terminalUI) activityEditPages(view *activityui.View, seq uint64, path string) ([]activitypresentation.DetailPage, string) {
	for _, entry := range u.main.activitySource(view).Entries {
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
		if u.main != nil {
			result := u.main.session.activity.ActivityDetail(u.main.session.Scope(), request)
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
