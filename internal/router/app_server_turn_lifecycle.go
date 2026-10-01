package router

import (
	"slices"
	"time"

	"github.com/yusing/mekugi/internal/ui/diffview"
)

type liveDiffPreviewTurn struct {
	id       string
	complete bool
}

// Native turn completion retires display state even when interrupted tools
// never return. It neither finalizes capture nor stops a host-owned process.
func (u *appServerUI) endTurnPreviews(thread, turn string) {
	if u.proxy != nil {
		u.proxy.execWindows.stopTurnPreviews(thread, turn)
		if u.proxy.autoLiveDiff != nil {
			u.proxy.autoLiveDiff.events.setPreviewTurn(thread, turn, false)
			u.proxy.autoLiveDiff.finishTurn(u.session.cwd, thread, turn)
		}
	}
	if u.shell != nil {
		for _, id := range slices.Clone(u.shell.liveDock.Order) {
			view := u.shell.liveDock.Views[id]
			if view != nil && view.Current.Thread == thread && view.Current.Turn == turn {
				u.shell.preview(diffview.Preview{ID: id, Workspace: view.Current.Workspace, Thread: thread, Turn: turn})
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

func (b *liveDiffBroker) setPreviewTurn(thread, turn string, active bool) {
	if b == nil || thread == "" || turn == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.previewTurns == nil {
		b.previewTurns = make(map[string]liveDiffPreviewTurn)
	}
	previous, known := b.previewTurns[thread]
	if !active && known && previous.id != turn {
		return
	}
	if active && known && previous.id == turn {
		return
	}
	b.previewTurns[thread] = liveDiffPreviewTurn{id: turn, complete: !active}
	stale := func(p diffview.Preview) bool {
		return p.Thread == thread && p.Turn != "" && (p.Turn != turn || !active)
	}
	retired := make(map[string]diffview.Preview)
	for id, preview := range b.previews {
		if stale(preview) {
			delete(b.previews, id)
			retired[id] = preview
		}
	}
	for _, preview := range b.completedPreviews {
		if stale(preview) {
			retired[preview.ID] = preview
		}
	}
	// A finished input frame may already be queued but not yet displayed.
	// Replace it too, or draining the mailbox could reopen the interrupted dock.
	if b.subscriber != nil {
		for _, preview := range b.subscriber.previews {
			if stale(preview) {
				retired[preview.ID] = preview
			}
		}
	}
	b.completedPreviews = slices.DeleteFunc(b.completedPreviews, stale)
	for id, preview := range retired {
		b.emitPreviewLocked(diffview.Preview{ID: id, Workspace: preview.Workspace, Thread: thread, Turn: preview.Turn})
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
