package terminalsession

import (
	"strings"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

// Source: apps/mekugi/internal/router/app_server_composer.go:58:127@1b46b141.
// MoveDraft maps terminal keys and wrapped geometry to a caret or history intent.
func (m *Main) MoveDraft(d composer.Draft, sequence string) (at, recall int) {
	at = d.Cursor()
	vertical := sequence == "\x1b[A" || sequence == "\x1bOA" || sequence == "\x1b[B" || sequence == "\x1bOB"
	if !vertical {
		m.CursorColumn = nil
	}
	switch sequence {
	case "\x1b[D", "\x1bOD":
		at = d.Boundary(at, -1)
	case "\x1b[C", "\x1bOC":
		at = d.Boundary(at, 1)
	case "\x1b[1;5D", "\x1b[1;5C", "\x1b[1;3D", "\x1b[1;3C", "\x1bb", "\x1bf":
		at = d.WordBoundary(at, sequence == "\x1bb" || strings.HasSuffix(sequence, "D"))
	case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[1;5A":
		at = strings.LastIndex(d.Text[:at], "\n") + 1
	case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[1;5B":
		if next := strings.IndexByte(d.Text[at:], '\n'); next >= 0 {
			at += next
		} else {
			at = len(d.Text)
		}
	case "\x1b[A", "\x1bOA", "\x1b[B", "\x1bOB":
		_, points := DraftLayout(d, m.ComposerWidth)
		current := points[len(points)-1]
		for _, point := range points {
			if point.Offset == at {
				current = point
				break
			}
		}
		if m.CursorColumn == nil {
			m.CursorColumn = new(current.Column)
		}
		row := current.Row + 1
		if strings.HasSuffix(sequence, "A") {
			row = current.Row - 1
		}
		best := -1
		for _, point := range points {
			if point.Row != row {
				continue
			}
			if best < 0 {
				best = point.Offset
			}
			if point.Column <= *m.CursorColumn {
				best = point.Offset
			}
		}
		if best >= 0 {
			at = best
		} else {
			if strings.HasSuffix(sequence, "A") {
				return at, -1
			}
			return at, 1
		}
	}
	for _, span := range d.Spans() {
		start, end := span.Start, span.End
		if at > start && at < end {
			if at > d.Cursor() {
				at = end
			} else {
				at = start
			}
			break
		}
	}
	return at, 0
}
