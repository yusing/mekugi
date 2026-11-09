package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

func insertDraft(attached *attachedPresentation, editor *composer.Editor, clock func() time.Time, owned map[string]bool, questions *nativeQuestionDock, approvals *nativeApprovalDock, side *sessioncontrol.Side, text string) {
	setNotice(&attached.feedback, presentationTime(clock), "", false)
	editor.Insert(text)
	pruneDraftImages(owned, editor, attached.conversation, questions, approvals, side, attached.hooks.nativeImages)
}

func (session *nativeSession) deleteDraftRange(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, side *sessioncontrol.Side, clock func() time.Time, overlay bool, start int, end int) {
	if attached.view != "" {
		(*editor).Delete(start, end, composer.NoRun)
		return
	}
	if start == end {
		(*editor).Run = composer.NoRun
		return
	}
	if session.questions.Current() != nil || session.approvals.Open {
		removeDraft(attached, (*editor), clock, session.ownedImages, &session.questions, &session.approvals, side, start, end, composer.NoRun)
		return
	}
	view := publishPresentation(attached, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, side)
	defer func() {
		settlePresentation(attached, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, side, clock, &session.questions, &session.approvals, overlay)
	}()
	if _, err := attached.presentation.ApplyEditor(sessionpresentation.EditorIntent{Version: view.Version, Target: view.EditorTarget(), Action: composer.Action{Kind: composer.DeleteAction, Start: start, End: end}}, attached.conversation); err != nil {
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return
	}
	setNotice(&attached.feedback, presentationTime(clock), "", false)
	pruneDraftImages(session.ownedImages, *editor, attached.conversation, &session.questions, &session.approvals, side, attached.hooks.nativeImages)
}

func removeDraft(attached *attachedPresentation, editor *composer.Editor, clock func() time.Time, owned map[string]bool, questions *nativeQuestionDock, approvals *nativeApprovalDock, side *sessioncontrol.Side, start, end int, run composer.Run) {
	editor.Delete(start, end, run)
	if start == end {
		return
	}
	setNotice(&attached.feedback, presentationTime(clock), "", false)
	pruneDraftImages(owned, editor, attached.conversation, questions, approvals, side, attached.hooks.nativeImages)
}
