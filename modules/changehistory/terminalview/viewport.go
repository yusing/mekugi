package terminalview

import (
	"slices"

	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

// Source: internal/livediff/model.go:42:152,399:461@[ebde9ad5]
// View owns file-local scroll state over the semantic review.
type View struct {
	presentation.View
	Scroll map[string]int
}

func (v *View) Update(publication presentation.Publication) {
	selected := ""
	if len(v.Files) > 0 {
		selected = v.Files[v.Selected].Key()
	}
	v.View.Update(publication)
	if selected != "" && len(v.Files) > 0 && v.Scroll != nil {
		key := v.Files[v.Selected].Key()
		if key != selected && slices.ContainsFunc(v.Files[v.Selected].Chunks, func(chunk presentation.Chunk) bool { return chunk.Key == selected }) {
			v.Scroll[key] = v.Scroll[selected]
		}
	}
}

// Keep the viewport anchored to a file and its local row when preceding files grow.
// Scrolling out of a file forgets its position: only a file the viewport left by
// a jump (n/p, Enter, JumpTo) reopens where it was, never where a scroll passed through.
func (v *View) ScrollTo(render Render, offset int) {
	v.moveTo(render, offset, true)
}

// JumpTo moves the viewport like ScrollTo, but a file it leaves keeps its position.
func (v *View) JumpTo(render Render, offset int) {
	v.moveTo(render, offset, false)
}

func (v *View) moveTo(render Render, offset int, forget bool) {
	// Events and keys can arrive before the next paint. Never use absent or
	// stale file geometry to change the viewport's file-local positions.
	if len(v.Files) == 0 || len(render.Starts) != len(v.Files) {
		return
	}
	offset = max(0, min(offset, len(render.Lines)-1))
	previous := v.Selected
	v.Selected = 0
	for i, start := range render.Starts {
		if start > offset {
			break
		}
		end := len(render.Lines)
		if i+1 < len(render.Starts) {
			end = render.Starts[i+1]
		}
		if end > start {
			v.Selected = i
		}
	}
	if forget && previous != v.Selected && previous < len(v.Files) {
		delete(v.Scroll, v.Files[previous].Key())
	}
	v.Scroll[v.Files[v.Selected].Key()] = max(0, offset-render.Starts[v.Selected])
}

// Open selects a file at its header, as an explicit navigation target.
func (v *View) Open(file int) {
	if file < 0 || file >= len(v.Files) {
		return
	}
	v.Selected = file
	if v.Scroll != nil {
		delete(v.Scroll, v.Files[file].Key())
	}
}

// Reflow saved positions when wrapping changes, keeping the same logical row.
func (v *View) Reflow(before, after Render) {
	for i, file := range v.Files {
		if i >= len(before.Starts) || len(before.RowStarts) == 0 {
			continue
		}
		offset := before.Starts[i] + v.Scroll[file.Key()]
		row, exact := slices.BinarySearch(before.RowStarts, offset)
		if !exact {
			row--
		}
		if row >= 0 && row < len(after.RowStarts) {
			v.Scroll[file.Key()] = max(0, after.RowStarts[row]-after.Starts[i])
		}
	}
}
