package terminalview

import (
	"slices"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
)

// Selection freezes the last painted source at a pane-local zero-based cell.
// Non-source cells return an empty value without copying the document.
// Source: apps/mekugi/internal/router/terminal_selection.go:231:269@[7dc6daaf]
func (c *Pane) Selection(x, y int) terminalui.SelectionSource {
	if c.failure != "" {
		return terminalui.SelectionSource{}
	}
	source := c.selection
	if x < source.X || x >= source.X+source.Width || y < source.Y || y >= source.Y+len(source.Painted) {
		return terminalui.SelectionSource{}
	}
	source.Painted = slices.Clone(source.Painted)
	if heading := c.selectionHeading; heading >= 0 {
		source.Document = append([]string{source.Document[heading]}, source.Document...)
		source.Sources = append([]diffrender.LineSource{source.Sources[heading]}, source.Sources...)
		source.Fixed = 1
	} else {
		source.Document, source.Sources = slices.Clone(source.Document), slices.Clone(source.Sources)
	}
	return source
}
