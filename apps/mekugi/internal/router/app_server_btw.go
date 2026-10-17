package router

import (
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

// sideDock retains only terminal interaction state for the published side view.
type sideDock struct {
	scroll, rows int
	rect         terminalsession.Rect
	copyAnswer   []string
}

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
		u.btw = new(sideDock)
	}
	b := u.side
	// Remove the command through the composer so attachment offsets stay valid.
	prefix := strings.Index(u.Text, "/btw") + len("/btw")
	u.deleteDraftRange(0, prefix)
	b.Begin(question, u.takeDraft())
	u.btw.scroll = 0
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
	update, err := u.requests.StartSide(b, sessioncontrol.SideMain{
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
	u.side, u.btw = nil, nil
	b.Close()
	u.conversation().Side = nil
	u.pruneDraftImages()
	return u.requests.ReleaseSide(b)
}

func (u *appServerUI) btwKey(key string) (bool, error) {
	b := u.btw
	if b == nil {
		return false, nil
	}
	switch key {
	case "\x1b":
		return true, u.closeBTW()
	case "\x1b[5~":
		b.scroll += max(1, b.rows-1)
		return true, nil
	case "\x1b[6~":
		b.scroll = max(0, b.scroll-max(1, b.rows-1))
		return true, nil
	}
	return false, nil
}
