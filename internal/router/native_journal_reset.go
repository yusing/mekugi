package router

import (
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func (u *appServerUI) ensureJournalReset() {
	if u.reset != nil || u.proxy == nil || u.proxy.replayStore == nil || u.journal == nil || u.journal.workspace == "" {
		return
	}
	u.journal.mu.Lock()
	ready := u.journal.tree != nil
	u.journal.mu.Unlock()
	if !ready {
		return
	}
	u.reset = &journalResetDriver{ctx: u.ctx, proxy: u.proxy, client: u.client, workspace: u.journal.workspace, thread: u.thread, delay: 3 * time.Second}
	if err := u.reset.restore(); err != nil {
		u.setNotice("Slice reset recovery: "+err.Error(), true)
	}
	u.showResetNotice()
}

func (u *appServerUI) showResetNotice() {
	if u.reset != nil && u.reset.notice != "" {
		u.setNotice(u.reset.notice, true)
		u.reset.notice = ""
	}
}

func (u *appServerUI) tickJournalReset(now time.Time) error {
	u.ensureJournalReset()
	if u.reset == nil {
		return nil
	}
	err := u.reset.tick(now)
	u.dirty = u.dirty || u.reset.active()
	u.showResetNotice()
	return err
}

func (u *appServerUI) journalResetStrip(width int) string {
	if !u.reset.active() {
		return ""
	}
	return ansi.Truncate(livediff.Safe(u.reset.label(time.Now()), false), width, "…")
}
