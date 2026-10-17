package router

import (
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func (u *appServerUI) submitBTW() error {
	text := strings.TrimSpace(u.Text)
	question := strings.TrimSpace(strings.TrimPrefix(text, "/btw"))
	if question == "" {
		u.setNotice("Use /btw QUESTION · repeat /btw for a follow-up · Esc closes", false)
		return nil
	}
	if u.Thread == "" || u.restoring != nil || u.Replacement.Pending() {
		u.setNotice("Wait for the conversation to load before asking a side question", false)
		return nil
	}
	if u.side != nil && (u.side.Busy || u.side.Starting) {
		u.setNotice("Side answer still running · wait for it or Esc to close", false)
		return nil
	}
	if u.side != nil && u.side.Status == conversationmodel.SideCompactionRequired {
		u.setNotice(conversationmodel.SideCompactionRequired, true)
		return nil
	}
	if u.side == nil {
		u.conversation().Side = new(conversationmodel.SideAnswer)
		u.side = &sessioncontrol.Side{SideAnswer: u.conversation().Side}
	}
	b := u.side
	// Remove the command through the composer so attachment offsets stay valid.
	prefix := strings.Index(u.Text, "/btw") + len("/btw")
	u.deleteDraftRange(0, prefix)
	b.Begin(question, u.takeDraft())
	b.Busy = true
	u.setNotice("", false)
	return u.flushBTW()
}

func (u *appServerUI) flushBTW() error {
	b := u.side
	if !b.Ready(u.Replacement.Pending()) {
		return nil
	}
	parts := []conversationmodel.Input{b.Pending}
	if u.waitForSkillBindings(parts) {
		return nil
	}
	u.bindSkills(&parts[0], true)
	u.snapshotDraftSkills(&parts[0])
	b.Pending = parts[0]
	update, err := u.Requests.StartSide(b, sessioncontrol.SideMain{
		Thread: u.Thread, Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier,
		Blocked: u.Starting() || u.InputPending() || u.Settings.Pending(), Replacing: u.Replacement.Pending(),
	}, u.now())
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
		u.restoreDrafts(update.Restore)
		u.setNotice("Side question restored ahead of draft · edit before resending", true)
	}
	if update.Notice != "" {
		u.setNotice(update.Notice, true)
	}
}

func (u *appServerUI) closeBTW() error {
	b := u.side
	if b == nil {
		return nil
	}
	u.side = nil
	b.Close()
	u.conversation().Side = nil
	u.pruneDraftImages()
	return u.Requests.ReleaseSide(b)
}

func (u *appServerUI) applyCloseSide(in sessionpresentation.CloseSideIntent) error {
	defer u.settlePresentation()
	if err := u.sessionPresentation().AdmitCloseSide(in); err != nil {
		u.setNotice(err.Error(), true)
		return nil
	}
	return u.closeBTW()
}
