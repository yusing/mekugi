package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func (u *appServerUI) insertDraft(text string) {
	u.shell.Main.CursorColumn = nil
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "", false)
	u.Editor.Insert(text)
	pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side)
}

func (u *appServerUI) deleteDraftRange(start, end int) {
	if start == end {
		u.Run = composer.NoRun
		return
	}
	if u.questions.Current() != nil || u.approvals.Open {
		u.removeDraft(start, end, composer.NoRun)
		return
	}
	view := u.publishPresentation(presentationTime(u.clock))
	u.editInput(sessionpresentation.DraftEditIntent{Version: view.Version, Revision: view.Draft.Revision, Start: start, End: end})
}

func (u *appServerUI) removeDraft(start, end int, run composer.Run) {
	u.Editor.Delete(start, end, run)
	if start == end {
		return
	}
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "", false)
	u.shell.Main.CursorColumn = nil
	pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side)
}
