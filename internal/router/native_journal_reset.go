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
	if u.questionCount() > 0 && u.reset.cancellable() {
		return u.reset.cancel()
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

// Match the reset's own turn, not the configured mode: ordinary provider
// compactions can still occur in a session that uses journal resets.
func (u *appServerUI) journalResetTurn(thread, turn string) bool {
	if turn == "" || thread != u.thread {
		return false
	}
	if u.reset != nil && u.reset.thread == thread && u.reset.compactTurn == turn {
		return true
	}
	return u.journalResetEvent(thread, turn)
}

func (u *appServerUI) journalResetCompleted(thread, turn string) bool {
	if turn == "" || thread != u.thread {
		return false
	}
	if u.journalResetEvent(thread, turn) {
		return true
	}
	d := u.reset
	if d == nil || d.thread != thread || d.compactTurn != turn || d.intent == nil {
		return false
	}
	// Router completion persists consumption before host item/completed, but
	// does not publish a journal mutation. Read that evidence, not dispatch intent.
	intent, err := d.proxy.replayStore.resetIntent(d.ctx, d.workspace, thread)
	return err == nil && intent != nil && intent.ID == d.intent.ID && intent.Phase == "consumed"
}

func (u *appServerUI) journalCompactionAnswered(thread, turn, item string) bool {
	if turn == "" || item == "" || thread != u.thread {
		return false
	}
	if u.proxy != nil && u.proxy.replayStore != nil {
		for _, sink := range []*nativeJournalSink{u.journal, u.unscopedJournal} {
			if sink != nil && sink.thread == thread {
				u.proxy.replayStore.bindStandaloneCompactionItem(u.ctx, sink.workspace, thread, turn, item)
				if u.proxy.replayStore.answeredCompactionItem(u.ctx, sink.workspace, thread, turn, item) {
					return true
				}
			}
		}
	}
	return false
}

// Auto mode attempts journal synthesis for ordinary compactions too. Dispatch
// wording expresses that intent; only an exact receipt changes completion.
func (u *appServerUI) compactionProgressText() string {
	if u.proxy != nil && u.proxy.journalCompaction == "auto" {
		return "Resetting context from journal if available"
	}
	return "Compacting context"
}

func (u *appServerUI) journalResetEvent(thread, turn string) bool {
	if turn == "" || thread != u.thread {
		return false
	}
	if u.journal != nil {
		u.journal.mu.Lock()
		defer u.journal.mu.Unlock()
		if u.journal.tree != nil {
			for _, event := range u.journal.tree.Events {
				if event.Op == "reset" && event.ResetTurn == turn {
					return true
				}
			}
		}
	}
	return false
}
