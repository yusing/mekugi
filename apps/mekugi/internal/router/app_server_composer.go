package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func (u *appServerUI) insertDraft(text string) {
	u.mainView.CursorColumn = nil
	u.setNotice("", false)
	u.Editor.Insert(text)
	u.pruneDraftImages()
}

func (u *appServerUI) deleteDraftRange(start, end int) {
	if start == end {
		u.Run = composer.NoRun
		return
	}
	if u.currentQuestion() != nil || u.approvals.Open {
		u.removeDraft(start, end, composer.NoRun)
		return
	}
	view := u.publishPresentation(u.now())
	u.editInput(sessionpresentation.DraftEditIntent{Version: view.Version, Revision: view.Draft.Revision, Start: start, End: end})
}

func (u *appServerUI) removeDraft(start, end int, run composer.Run) {
	u.Editor.Delete(start, end, run)
	if start == end {
		return
	}
	u.setNotice("", false)
	u.mainView.CursorColumn = nil
	u.pruneDraftImages()
}
