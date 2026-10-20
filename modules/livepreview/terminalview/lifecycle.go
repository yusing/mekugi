package terminalview

import (
	"maps"
	"slices"
	"time"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

const (
	CompletionHold = 1500 * time.Millisecond
	RevealDelay    = 300 * time.Millisecond
)

type pendingPreview struct {
	preview previewmodel.Preview
	since   time.Time
	order   uint64
}

// Source: apps/mekugi/internal/router/native_shell.go:25:34,54:132@630ce384.
// Observe receives only admitted snapshots. It grants no workspace authority.
func (p *PreviewPane) Observe(preview previewmodel.Preview, restored bool, now time.Time) {
	if restored && preview.Complete && p.Views[preview.ID] == nil {
		delete(p.pending, preview.ID)
		return
	}
	if preview.Caller == "" && preview.Workspace != "" {
		preview.Caller = "/root"
	}
	if p.Views[preview.ID] == nil && !slices.Contains(p.Callers(), preview.Caller) {
		if preview.Complete || preview.Workspace == "" || preview.Status == "" && len(preview.Files) == 0 && preview.Input == "" {
			delete(p.pending, preview.ID)
			return
		}
		if p.pending == nil {
			p.pending = make(map[string]pendingPreview)
		}
		pending, exists := p.pending[preview.ID]
		if !exists {
			p.sequence++
			pending.since, pending.order = now, p.sequence
		}
		pending.preview = preview
		p.pending[preview.ID] = pending
		return
	}
	delete(p.pending, preview.ID)
	p.Update(preview, now)
}

func (p *PreviewPane) Tick(now time.Time) bool {
	for _, pending := range slices.SortedFunc(maps.Values(p.pending), func(a, b pendingPreview) int {
		if a.order < b.order {
			return -1
		}
		if a.order > b.order {
			return 1
		}
		return 0
	}) {
		if now.Sub(pending.since) >= RevealDelay {
			p.Update(pending.preview, now)
			delete(p.pending, pending.preview.ID)
		}
	}
	changed := p.ExpireBatches(now, CompletionHold)
	return changed || len(p.Order) > 0 || len(p.pending) > 0
}

func (p *PreviewPane) Resync(scope previewmodel.Scope, ids []string) {
	retained := func(preview previewmodel.Preview, complete bool) bool {
		return scope.Workspaces[preview.Workspace][preview.Thread] && (complete || slices.Contains(ids, preview.ID))
	}
	p.Order = slices.DeleteFunc(p.Order, func(id string) bool {
		view := p.Views[id]
		if retained(view.Current, view.Complete) {
			return false
		}
		delete(p.Views, id)
		return true
	})
	for id, pending := range p.pending {
		if !retained(pending.preview, false) {
			delete(p.pending, id)
		}
	}
}

// EndTurn retires presentation only, independently of host execution and capture.
// Source: apps/mekugi/internal/router/app_server_turn_lifecycle.go:17:29@630ce384.
func (p *PreviewPane) EndTurn(thread, turn string, now time.Time) {
	for id, pending := range p.pending {
		if pending.preview.Thread == thread && pending.preview.Turn == turn {
			delete(p.pending, id)
		}
	}
	for _, id := range slices.Clone(p.Order) {
		view := p.Views[id]
		if view != nil && view.Current.Thread == thread && view.Current.Turn == turn {
			p.Update(previewmodel.Preview{ID: id, Workspace: view.Current.Workspace, Thread: thread, Turn: turn}, now)
		}
	}
}
