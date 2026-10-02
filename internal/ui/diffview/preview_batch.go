package diffview

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
)

// Native batches retain calls, but give every file its own source viewport.
// The cache belongs to the caller and is discarded with its last call.
type previewBatch struct {
	files    map[previewFileKey]*PreviewView
	order    []previewFileKey
	selected previewFileKey
	pinned   bool
}

type previewFileKey struct{ call, path string }

func (p *PreviewPane) batch(caller string) *previewBatch {
	if p.batches == nil {
		p.batches = make(map[string]*previewBatch)
	}
	b := p.batches[caller]
	if b == nil {
		b = &previewBatch{files: make(map[previewFileKey]*PreviewView)}
		p.batches[caller] = b
	}
	for _, id := range p.Order {
		call := p.Views[id]
		if call.Current.Caller != caller {
			continue
		}
		files := call.Current.Files
		if len(files) == 0 {
			files = []mekugi.ReviewFile{{}}
		}
		for _, file := range files {
			key := previewFileKey{id, cmp.Or(file.AfterPath, file.BeforePath)}
			view := b.files[key]
			if view == nil {
				view = &PreviewView{}
				b.files[key] = view
				b.order = append(b.order, key)
				if !b.pinned {
					b.selected = key
				}
			}
			current := call.Current
			if key.path != "" {
				current.Files = []mekugi.ReviewFile{file}
			}
			view.Current, view.Complete = current, call.Complete
		}
	}
	// A streaming program can move to another file within the same call.
	// Retain its previous file projections until the call is explicitly withdrawn.
	b.order = slices.DeleteFunc(b.order, func(key previewFileKey) bool {
		call := p.Views[key.call]
		if call == nil || key.path == "" && len(call.Current.Files) > 0 {
			delete(b.files, key)
			return true
		}
		b.files[key].Complete = call.Complete
		return false
	})
	if len(b.order) > 0 && !slices.Contains(b.order, b.selected) {
		b.selected, b.pinned = b.order[len(b.order)-1], false
	}
	return b
}

// RenderBatch uses a newest-following window rather than shrinking older files.
// Cycling pins that window, so arriving edits never steal a manual selection.
// minRows includes the file heading. A smaller terminal shows only a summary.
func (p *PreviewPane) RenderBatch(ctx context.Context, caller, workspace string, theme livediff.Theme, width, height, minRows int) ([]string, error) {
	b := p.batch(caller)
	count := len(b.order)
	if count == 0 || height <= 0 {
		return nil, nil
	}
	for _, view := range b.files {
		view.rows = 0
	}
	slots := min(count, max(0, (height-1)/minRows))
	noun := "files"
	if count == 1 {
		noun = "file"
	}
	label := "LIVE"
	if tool := b.files[b.selected].Current.Tool; tool != "" {
		switch tool {
		case "exec_command":
			tool = "shell"
		case "exec":
			tool = "Code Mode"
		}
		label += " · " + livediff.Safe(tool, false)
	}
	label += fmt.Sprintf(" · %d %s", count, noun)
	if slots == 0 {
		return []string{ansi.Truncate(theme.Accent()+label+" · enlarge for diff\x1b[0m", width, "…")}, nil
	}
	at := slices.Index(b.order, b.selected)
	start := min(max(0, at-slots+1), count-slots)
	if slots < count {
		label += fmt.Sprintf(" · %d–%d/%d · ^B e next", start+1, start+slots, count)
	}
	if b.pinned {
		label += " · pinned"
	}
	lines := []string{ansi.Truncate(theme.Accent()+label+"\x1b[0m", width, "…")}
	motion := p.Motion
	if motion.Enabled {
		motion.Now = time.Now()
	}
	for i, key := range b.order[start : start+slots] {
		rows := (height - len(lines)) / (slots - i)
		part, err := b.files[key].Render(ctx, workspace, theme, width, rows, motion)
		if err != nil {
			return nil, err
		}
		lines = append(lines, part...)
		lines = append(lines, make([]string, max(0, rows-len(part)))...)
	}
	return lines, nil
}

func (p *PreviewPane) NextBatch(caller string) bool {
	b := p.batch(caller)
	if len(b.order) < 2 {
		return false
	}
	at := slices.Index(b.order, b.selected)
	b.selected, b.pinned = b.order[(at+1)%len(b.order)], true
	return true
}

// ExpireBatches closes a caller's entire burst together after its last update
// has settled. Other callers neither retain nor prematurely close that burst.
func (p *PreviewPane) ExpireBatches(now time.Time, hold time.Duration) bool {
	changed := false
	for _, caller := range p.Callers() {
		live, latest := false, time.Time{}
		for _, id := range p.Order {
			view := p.Views[id]
			if view.Current.Caller != caller {
				continue
			}
			live = live || !view.Complete
			if view.updated.After(latest) {
				latest = view.updated
			}
		}
		if live || now.Sub(latest) < hold {
			continue
		}
		p.Order = slices.DeleteFunc(p.Order, func(id string) bool {
			if p.Views[id].Current.Caller != caller {
				return false
			}
			delete(p.Views, id)
			return true
		})
		delete(p.batches, caller)
		changed = true
	}
	// Turn interruption and withdrawal can remove the final call directly.
	for caller := range p.batches {
		if !slices.Contains(p.Callers(), caller) {
			delete(p.batches, caller)
		}
	}
	return changed
}

// ScrollBatch moves only the displayed file windows, independently of transcripts
// and captured diffs. Resuming follow also releases the manually pinned window.
func (p *PreviewPane) ScrollBatch(caller string, key byte) bool {
	b := p.batches[caller]
	if b == nil {
		return false
	}
	if _, _, ok := terminalui.PaneScroll(key, 0, 1, 1); !ok {
		return false
	}
	if key == 'r' {
		b.pinned = false
		if len(b.order) > 0 {
			b.selected = b.order[len(b.order)-1]
		}
	}
	for _, view := range b.files {
		if key == 'r' {
			view.Paused = false
			continue
		}
		if view.rows == 0 {
			continue
		}
		at := view.ScrollRow
		if !view.Paused {
			at = view.Focus
		}
		next, follow, _ := terminalui.PaneScroll(key, at, view.rows, len(view.Source))
		view.ScrollRow, view.Paused = next, !follow
	}
	return true
}
