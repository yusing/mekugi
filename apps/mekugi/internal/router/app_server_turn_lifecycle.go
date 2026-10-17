package router

import (
	"slices"
	"time"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
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
	for _, progress := range u.session.waits.EndTurn(u.session.Transcript(thread, turn).Turn) {
		value := progress.Value
		entries = append(entries, activityPaneEntry{
			Seq: u.session.next(), Agent: u.session.path(thread), Kind: "progress",
			Text: (activityui.Block{Value: value}).ProgressText(), Observed: now,
			native: &liveActivityNativeItem{thread: thread, turn: turn, item: progress.ID, phase: "turn/completed", wait: &value, live: value.Live},
		})
	}
	return entries
}
