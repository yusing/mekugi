package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func insertDraft(attached *attachedPresentation, editor *composer.Editor, terminal *terminalsession.Attachment, clock func() time.Time, owned map[string]bool, questions *nativeQuestionDock, approvals *nativeApprovalDock, side *sessioncontrol.Side, text string) {
	terminal.Main.CursorColumn = nil
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	editor.Insert(text)
	pruneDraftImages(owned, editor, attached.conversation, questions, approvals, terminal.Prompts, side)
}

func (u *appServerUI) deleteDraftRange(start, end int) {
	if start == end {
		u.Run = composer.NoRun
		return
	}
	if u.questions.Current() != nil || u.approvals.Open {
		removeDraft(&u.attached, u.Editor, u.shell, u.clock, u.ownedImages, &u.questions, &u.approvals, u.side, start, end, composer.NoRun)
		return
	}
	view := u.publishPresentation(presentationTime(u.clock))
	u.editInput(sessionpresentation.DraftEditIntent{Version: view.Version, Revision: view.Draft.Revision, Start: start, End: end})
}

func removeDraft(attached *attachedPresentation, editor *composer.Editor, terminal *terminalsession.Attachment, clock func() time.Time, owned map[string]bool, questions *nativeQuestionDock, approvals *nativeApprovalDock, side *sessioncontrol.Side, start, end int, run composer.Run) {
	editor.Delete(start, end, run)
	if start == end {
		return
	}
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	terminal.Main.CursorColumn = nil
	pruneDraftImages(owned, editor, attached.conversation, questions, approvals, terminal.Prompts, side)
}
