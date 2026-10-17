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

func (u *appServerUI) deleteDraft(backward bool) {
	u.mainView.CursorColumn = nil
	at := u.Cursor()
	start, end, run := at, u.Boundary(at, 1), composer.DeleteRun
	if backward {
		start, end, run = u.Boundary(at, -1), at, composer.BackspaceRun
	}
	u.removeDraft(start, end, run)
}

func (u *appServerUI) deleteWord(backward bool) {
	u.mainView.CursorColumn = nil
	at := u.Cursor()
	other := u.WordBoundary(at, backward)
	u.deleteDraftRange(min(at, other), max(at, other))
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

func (u *appServerUI) moveDraft(sequence string) {
	u.Run = composer.NoRun
	at, history := u.mainView.MoveDraft(u.Draft, sequence)
	if history != 0 {
		u.recallInput(history < 0)
		return
	}
	u.CursorBack = len(u.Text) - at
}
