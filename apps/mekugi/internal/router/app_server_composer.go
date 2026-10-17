package router

import (
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func (u *appServerUI) insertDraft(text string) {
	u.mainView.CursorColumn = nil
	u.setNotice("", false)
	u.Editor.Insert(text)
	u.pruneDraftImages()
}

func (u *appServerUI) deleteDraft(backward bool) {
	u.mainView.CursorColumn = nil
	at := u.Cursor()
	start, end, run := at, u.Boundary(at, 1), composer.DeleteRun
	if backward {
		start, end, run = u.Boundary(at, -1), at, composer.BackspaceRun
	}
	u.removeDraft(start, end, run)
}

func (u *appServerUI) deleteWord(backward bool) {
	u.mainView.CursorColumn = nil
	at := u.Cursor()
	other := u.WordBoundary(at, backward)
	u.deleteDraftRange(min(at, other), max(at, other))
}

func (u *appServerUI) deleteDraftRange(start, end int) { u.removeDraft(start, end, composer.NoRun) }

func (u *appServerUI) removeDraft(start, end int, run composer.Run) {
	u.Editor.Delete(start, end, run)
	if start == end {
		return
	}
	u.setNotice("", false)
	u.mainView.CursorColumn = nil
	u.pruneDraftImages()
}

func (u *appServerUI) moveDraft(sequence string) {
	u.Run = composer.NoRun
	at := u.Cursor()
	vertical := sequence == "\x1b[A" || sequence == "\x1bOA" || sequence == "\x1b[B" || sequence == "\x1bOB"
	if !vertical {
		u.mainView.CursorColumn = nil
	}
	switch sequence {
	case "\x1b[D", "\x1bOD":
		at = u.Boundary(at, -1)
	case "\x1b[C", "\x1bOC":
		at = u.Boundary(at, 1)
	case "\x1b[1;5D", "\x1b[1;5C", "\x1b[1;3D", "\x1b[1;3C", "\x1bb", "\x1bf":
		at = u.WordBoundary(at, sequence == "\x1bb" || strings.HasSuffix(sequence, "D"))
	case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[1;5A":
		at = strings.LastIndex(u.Text[:at], "\n") + 1
	case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[1;5B":
		if next := strings.IndexByte(u.Text[at:], '\n'); next >= 0 {
			at += next
		} else {
			at = len(u.Text)
		}
	case "\x1b[A", "\x1bOA", "\x1b[B", "\x1bOB":
		_, points := terminalsession.DraftLayout(u.Draft, u.mainView.ComposerWidth)
		current := points[len(points)-1]
		for _, point := range points {
			if point.Offset == at {
				current = point
				break
			}
		}
		if u.mainView.CursorColumn == nil {
			u.mainView.CursorColumn = new(current.Column)
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
			if point.Column <= *u.mainView.CursorColumn {
				best = point.Offset
			}
		}
		if best >= 0 {
			at = best
		} else {
			u.recallInput(strings.HasSuffix(sequence, "A"))
			return
		}
	}
	for _, span := range u.Spans() {
		start, end := span.Start, span.End
		if at > start && at < end {
			if at > u.Cursor() {
				at = end
			} else {
				at = start
			}
			break
		}
	}
	u.CursorBack = len(u.Text) - at
}
