package launch

import (
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func submitBTW(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, owned *map[string]bool, environment []string) error {
	text := strings.TrimSpace((*editor).Text)
	question := strings.TrimSpace(strings.TrimPrefix(text, "/btw"))
	if question == "" {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Use /btw QUESTION · repeat /btw for a follow-up · Esc closes", false)
		return nil
	}
	if control.Thread == "" || restoring || control.Replacement.Pending() {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the conversation to load before asking a side question", false)
		return nil
	}
	if (*side) != nil && ((*side).Busy || (*side).Starting) {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Side answer still running · wait for it or Esc to close", false)
		return nil
	}
	if (*side) != nil && (*side).Status == conversationmodel.SideCompactionRequired {
		setNotice(&attached.feedback, terminal, presentationTime(clock), conversationmodel.SideCompactionRequired, true)
		return nil
	}
	if (*side) == nil {
		attached.conversation.Side = new(conversationmodel.SideAnswer)
		(*side) = &sessioncontrol.Side{SideAnswer: attached.conversation.Side}
	}
	b := (*side)
	// Remove the command through the composer so attachment offsets stay valid.
	prefix := strings.Index((*editor).Text, "/btw") + len("/btw")
	deleteDraftRange(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay, (*owned), 0, prefix)
	b.Begin(question, prepareDraft(picker, terminal, attached.cwd, environment, (*editor).Take()))
	b.Busy = true
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	return flushBTW(attached, terminal, control, picker, questions, approvals, clock, settings, side, owned, environment)
}

func flushBTW(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, side **sessioncontrol.Side, owned *map[string]bool, environment []string) error {
	b := (*side)
	if !b.Ready(control.Replacement.Pending()) {
		return nil
	}
	parts := []conversationmodel.Input{b.Pending}
	if picker.waitForSkillBindings(attached, terminal, clock, control.Requests, parts) {
		return nil
	}
	picker.bindSkills(attached.cwd, &parts[0], true)
	snapshotDraftSkills(attached.cwd, environment, &parts[0])
	b.Pending = parts[0]
	update, err := control.Requests.StartSide(b, sessioncontrol.SideMain{
		Thread: control.Thread, Model: settings.Model, Effort: settings.Effort, Tier: settings.Tier,
		Blocked: control.Starting() || control.InputPending() || control.Settings.Pending(), Replacing: control.Replacement.Pending(),
	}, presentationTime(clock))
	if update.TurnRequested {
		for _, image := range b.Pending.Images {
			delete((*owned), image.Path)
		}
	}
	applySideUpdate(attached, terminal, control, picker, questions, approvals, clock, update)
	return err
}

func applySideUpdate(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, update sessioncontrol.SideUpdate) {
	if update.Restore.Text != "" {
		restoreDrafts(attached, terminal, questions, approvals, picker, control.Turn, clock, update.Restore)
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Side question restored ahead of draft · edit before resending", true)
	}
	if update.Notice != "" {
		setNotice(&attached.feedback, terminal, presentationTime(clock), update.Notice, true)
	}
}

func closeBTW(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, side **sessioncontrol.Side, owned *map[string]bool) error {
	b := (*side)
	if b == nil {
		return nil
	}
	(*side) = nil
	b.Close()
	attached.conversation.Side = nil
	pruneDraftImages((*owned), (*editor), attached.conversation, questions, approvals, terminal.Prompts, (*side))
	return control.Requests.ReleaseSide(b)
}

func applyCloseSide(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, overlay bool, owned *map[string]bool, in sessionpresentation.CloseSideIntent) error {
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, childKey, settings, tiers, title, backend, restoring, (*side), clock, questions, approvals, overlay)
	}()
	if err := attached.presentation.AdmitCloseSide(in); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return nil
	}
	return closeBTW(attached, terminal, control, editor, questions, approvals, side, owned)
}
