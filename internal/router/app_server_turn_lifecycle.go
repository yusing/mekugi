package router

import (
	"slices"
	"time"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

// Native turn completion retires display state even when interrupted tools
// never return. It neither finalizes capture nor stops a host-owned process.
func (u *appServerUI) endTurnPreviews(thread, turn string) {
	if u.proxy != nil {
		u.proxy.execWindows.stopTurnPreviews(thread, turn)
		if live := u.proxy.review.Preview(); live != nil {
			live.SetTurn(thread, turn, false)
			live.FinishTurn(u.session.cwd, thread, turn)
		}
	}
	if u.shell != nil {
		for id, pending := range u.shell.livePending {
			if pending.preview.Thread == thread && pending.preview.Turn == turn {
				delete(u.shell.livePending, id)
			}
		}
		for _, id := range slices.Clone(u.shell.liveDock.Order) {
			view := u.shell.liveDock.Views[id]
			if view != nil && view.Current.Thread == thread && view.Current.Turn == turn {
				u.shell.preview(previewmodel.Preview{ID: id, Workspace: view.Current.Workspace, Thread: thread, Turn: turn}, false)
			}
		}
	}
}

func (r *execWindowRegistry) stopTurnPreviews(thread, turn string) {
	if r == nil || turn == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, window := range r.windows {
		if window.thread == thread && window.turn == turn && window.previewCancel != nil {
			window.previewCancel()
			window.previewCancel = nil
		}
	}
}

// A wait may have no item/completed notification after interruption. Close
// only that turn's presentation, without claiming any child has finished.
func (u *appServerUI) endTurnWaits(thread, turn string, now time.Time) []activityPaneEntry {
	var entries []activityPaneEntry
	for _, view := range []*liveActivityView{u.view, u.agents} {
		if view == nil {
			continue
		}
		for _, entry := range view.entries {
			n := entry.native
			if n == nil || n.thread != thread || n.turn != turn || n.wait == nil || (n.phase == "item/completed" || n.phase == "turn/completed") {
				continue
			}
			native, block := *n, *n.wait
			block.Body = "Wait ended"
			native.phase, native.wait = "turn/completed", &block
			entry.native, entry.Text = &native, block.ProgressText()
			entry.Seq, entry.Observed = u.session.next(), now
			entries = append(entries, entry.activityPaneEntry)
		}
	}
	return entries
}
