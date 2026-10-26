package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func insertDraft(attached *attachedPresentation, editor *composer.Editor, terminal *terminalsession.Attachment, clock func() time.Time, owned map[string]bool, questions *nativeQuestionDock, approvals *nativeApprovalDock, side *sessioncontrol.Side, text string) {
	terminal.Main.CursorColumn = nil
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	editor.Insert(text)
	pruneDraftImages(owned, editor, attached.conversation, questions, approvals, terminal.Prompts, side, attached.nativeImages)
}

func deleteDraftRange(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, owned map[string]bool, start, end int) {
	if attached.view != "" {
		(*editor).Delete(start, end, composer.NoRun)
		return
	}
	if start == end {
		(*editor).Run = composer.NoRun
		return
	}
	if questions.Current() != nil || approvals.Open {
		removeDraft(attached, (*editor), terminal, clock, owned, questions, approvals, side, start, end, composer.NoRun)
		return
	}
	view := publishPresentation(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, side)
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, settings, tiers, title, backend, restoring, side, clock, questions, approvals, overlay)
	}()
	if _, err := attached.presentation.ApplyEditor(sessionpresentation.EditorIntent{Version: view.Version, Target: view.EditorTarget(), Action: composer.Action{Kind: composer.DeleteAction, Start: start, End: end}}, attached.conversation); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return
	}
	terminal.Main.CursorColumn = nil
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	pruneDraftImages(owned, *editor, attached.conversation, questions, approvals, terminal.Prompts, side, attached.nativeImages)
}

func removeDraft(attached *attachedPresentation, editor *composer.Editor, terminal *terminalsession.Attachment, clock func() time.Time, owned map[string]bool, questions *nativeQuestionDock, approvals *nativeApprovalDock, side *sessioncontrol.Side, start, end int, run composer.Run) {
	editor.Delete(start, end, run)
	if start == end {
		return
	}
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	terminal.Main.CursorColumn = nil
	pruneDraftImages(owned, editor, attached.conversation, questions, approvals, terminal.Prompts, side, attached.nativeImages)
}
