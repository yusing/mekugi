package router

import (
	"strings"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func (u *appServerUI) submitBTW() error {
	text := strings.TrimSpace(u.Text)
	question := strings.TrimSpace(strings.TrimPrefix(text, "/btw"))
	if question == "" {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Use /btw QUESTION · repeat /btw for a follow-up · Esc closes", false)
		return nil
	}
	if u.Thread == "" || u.restoring != nil || u.Replacement.Pending() {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Wait for the conversation to load before asking a side question", false)
		return nil
	}
	if u.side != nil && (u.side.Busy || u.side.Starting) {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Side answer still running · wait for it or Esc to close", false)
		return nil
	}
	if u.side != nil && u.side.Status == conversationmodel.SideCompactionRequired {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), conversationmodel.SideCompactionRequired, true)
		return nil
	}
	if u.side == nil {
		u.attached.conversation.Side = new(conversationmodel.SideAnswer)
		u.side = &sessioncontrol.Side{SideAnswer: u.attached.conversation.Side}
	}
	b := u.side
	// Remove the command through the composer so attachment offsets stay valid.
	prefix := strings.Index(u.Text, "/btw") + len("/btw")
	deleteDraftRange(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil, u.ownedImages, 0, prefix)
	b.Begin(question, prepareDraft(&u.picker, u.shell, u.attached.cwd, u.skillEnvironment, u.Editor.Take()))
	b.Busy = true
	setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "", false)
	return u.flushBTW()
}

func (u *appServerUI) flushBTW() error {
	b := u.side
	if !b.Ready(u.Replacement.Pending()) {
		return nil
	}
	parts := []conversationmodel.Input{b.Pending}
	if u.picker.waitForSkillBindings(&u.attached, u.shell, u.clock, u.Requests, parts) {
		return nil
	}
	u.picker.bindSkills(u.attached.cwd, &parts[0], true)
	snapshotDraftSkills(u.attached.cwd, u.skillEnvironment, &parts[0])
	b.Pending = parts[0]
	update, err := u.Requests.StartSide(b, sessioncontrol.SideMain{
		Thread: u.Thread, Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier,
		Blocked: u.Starting() || u.InputPending() || u.Settings.Pending(), Replacing: u.Replacement.Pending(),
	}, presentationTime(u.clock))
	if update.TurnRequested {
		for _, image := range b.Pending.Images {
			delete(u.ownedImages, image.Path)
		}
	}
	u.applySideUpdate(update)
	return err
}

func (u *appServerUI) applySideUpdate(update sessioncontrol.SideUpdate) {
	if update.Restore.Text != "" {
		restoreDrafts(&u.attached, u.shell, &u.questions, &u.approvals, &u.picker, u.Turn, u.clock, update.Restore)
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Side question restored ahead of draft · edit before resending", true)
	}
	if update.Notice != "" {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), update.Notice, true)
	}
}

func (u *appServerUI) closeBTW() error {
	b := u.side
	if b == nil {
		return nil
	}
	u.side = nil
	b.Close()
	u.attached.conversation.Side = nil
	pruneDraftImages(u.ownedImages, u.Editor, u.attached.conversation, &u.questions, &u.approvals, u.shell.Prompts, u.side)
	return u.Requests.ReleaseSide(b)
}

func (u *appServerUI) applyCloseSide(in sessionpresentation.CloseSideIntent) error {
	defer func() {
		settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	}()
	if err := u.attached.presentation.AdmitCloseSide(in); err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
		return nil
	}
	return u.closeBTW()
}
