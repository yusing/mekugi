package router

import (
	"iter"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/yusing/mekugi/internal/livediff"
)

// cursorBack counts bytes after the caret, so an untouched draft starts at its end.
func (u *appServerUI) cursor() int { return len(u.draft) - min(u.cursorBack, len(u.draft)) }

// composerRun groups consecutive keystrokes into one undoable edit.
type composerRun uint8

const (
	runNone composerRun = iota
	runInsert
	runBackspace
	runDelete
)

func (u *appServerUI) insertDraft(text string) {
	u.cursorColumn = nil
	u.notice, u.noticeAlert = "", false
	at := u.cursor()
	// Typing undoes a word at a time: a word typed after whitespace starts a new edit.
	wordStart := false
	if at > 0 && text != "" {
		before, _ := utf8.DecodeLastRuneInString(u.draft[:at])
		first, _ := utf8.DecodeRuneInString(text)
		wordStart = unicode.IsSpace(before) && !unicode.IsSpace(first)
	}
	if u.run != runInsert || wordStart {
		u.recordDraft()
		u.run = runInsert
	}
	u.draft = u.draft[:at] + text + u.draft[at:]
	for i := range u.images {
		if u.images[i].start >= at {
			u.images[i].start += len(text)
			u.images[i].end += len(text)
		}
	}
}

func (u *appServerUI) draftBoundary(at, direction int) int {
	for _, attachment := range u.images {
		if direction < 0 && at > attachment.start && at <= attachment.end {
			return attachment.start
		}
		if direction > 0 && at >= attachment.start && at < attachment.end {
			return attachment.end
		}
	}
	previous := 0
	for start, text := range u.draftGraphemes() {
		end := start + len(text)
		if direction < 0 && end >= at {
			return previous
		}
		if direction > 0 && end > at {
			return end
		}
		previous = end
	}
	return len(u.draft)
}

func (u *appServerUI) deleteDraft(backward bool) {
	u.cursorColumn = nil
	at := u.cursor()
	start, end, run := at, u.draftBoundary(at, 1), runDelete
	if backward {
		start, end, run = u.draftBoundary(at, -1), at, runBackspace
	}
	u.removeDraft(start, end, run)
}

func (u *appServerUI) deleteWord(backward bool) {
	at := u.cursor()
	sequence := "\x1b[1;5C"
	if backward {
		sequence = "\x1b[1;5D"
	}
	u.moveDraft(sequence)
	other := u.cursor()
	u.cursorBack = len(u.draft) - at
	u.deleteDraftRange(min(at, other), max(at, other))
}

func (u *appServerUI) deleteDraftRange(start, end int) { u.removeDraft(start, end, runNone) }

// removeDraft deletes a range; consecutive deletions of the same run kind
// undo together, and any other range is its own edit.
func (u *appServerUI) removeDraft(start, end int, run composerRun) {
	if start == end {
		u.run = runNone
		return
	}
	if run == runNone || u.run != run {
		u.recordDraft()
	}
	u.run = run
	u.notice, u.noticeAlert = "", false
	u.cursorColumn = nil
	kept := u.images[:0]
	for _, attachment := range u.images {
		if attachment.start < end && attachment.end > start {
			continue
		}
		if attachment.start >= end {
			attachment.start -= end - start
			attachment.end -= end - start
		}
		kept = append(kept, attachment)
	}
	u.images = kept
	u.draft = u.draft[:start] + u.draft[end:]
	u.cursorBack = len(u.draft) - start
	u.renumberImages()
	u.pruneDraftImages()
}

// Attachment edges are grapheme boundaries even when adjacent text begins with
// a combining mark, so editing can never split a placeholder from its image.
func (u *appServerUI) draftGraphemes() iter.Seq2[int, string] {
	return func(yield func(int, string) bool) {
		boundaries := []int{0}
		for _, attachment := range u.images {
			boundaries = append(boundaries, attachment.start, attachment.end)
		}
		boundaries = append(boundaries, len(u.draft))
		for i := 1; i < len(boundaries); i++ {
			start, end := boundaries[i-1], boundaries[i]
			g := uniseg.NewGraphemes(u.draft[start:end])
			for g.Next() {
				offset, _ := g.Positions()
				if !yield(start+offset, g.Str()) {
					return
				}
			}
		}
	}
}

type composerPoint struct{ offset, row, column int }

// Layout and navigation share display-cell positions, including soft wraps.
func (u *appServerUI) draftLayout() ([]string, []composerPoint) {
	width := u.composerWidth
	if width == 0 {
		width = 80
	}
	rows := []string{""}
	points := []composerPoint{}
	column := 0
	imageIndex := 0
	for start, cluster := range u.draftGraphemes() {
		text := livediff.Safe(cluster, false)
		size := ansi.StringWidth(text)
		if column >= width || text != "\n" && column > 0 && column+size > width {
			rows = append(rows, "")
			column = 0
		}
		points = append(points, composerPoint{start, len(rows) - 1, column})
		if text == "\n" {
			rows = append(rows, "")
			column = 0
		} else {
			for imageIndex < len(u.images) && start >= u.images[imageIndex].end {
				imageIndex++
			}
			if imageIndex < len(u.images) && start >= u.images[imageIndex].start {
				text = "\x1b[1;36m" + text + "\x1b[22;39m"
			}
			rows[len(rows)-1] += text
			column += size
		}
	}
	if width > 1 && column >= width {
		rows = append(rows, "")
		column = 0
	}
	points = append(points, composerPoint{len(u.draft), len(rows) - 1, column})
	if width > 1 {
		rows[len(rows)-1] += " "
	}
	return rows, points
}

func (u *appServerUI) moveDraft(sequence string) {
	u.run = runNone
	at := u.cursor()
	vertical := sequence == "\x1b[A" || sequence == "\x1bOA" || sequence == "\x1b[B" || sequence == "\x1bOB"
	if !vertical {
		u.cursorColumn = nil
	}
	switch sequence {
	case "\x1b[D", "\x1bOD":
		at = u.draftBoundary(at, -1)
	case "\x1b[C", "\x1bOC":
		at = u.draftBoundary(at, 1)
	case "\x1b[1;5D", "\x1b[1;5C":
		at = u.wordBoundary(at, strings.HasSuffix(sequence, "D"))
	case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[1;5A":
		at = strings.LastIndex(u.draft[:at], "\n") + 1
	case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[1;5B":
		if next := strings.IndexByte(u.draft[at:], '\n'); next >= 0 {
			at += next
		} else {
			at = len(u.draft)
		}
	case "\x1b[A", "\x1bOA", "\x1b[B", "\x1bOB":
		_, points := u.draftLayout()
		current := points[len(points)-1]
		for _, point := range points {
			if point.offset == at {
				current = point
				break
			}
		}
		if u.cursorColumn == nil {
			u.cursorColumn = new(current.column)
		}
		row := current.row + 1
		if strings.HasSuffix(sequence, "A") {
			row = current.row - 1
		}
		best := -1
		for _, point := range points {
			if point.row != row {
				continue
			}
			if best < 0 {
				best = point.offset
			}
			if point.column <= *u.cursorColumn {
				best = point.offset
			}
		}
		if best >= 0 {
			at = best
		}
	}
	for _, attachment := range u.images {
		if at > attachment.start && at < attachment.end {
			if at > u.cursor() {
				at = attachment.end
			} else {
				at = attachment.start
			}
			break
		}
	}
	u.cursorBack = len(u.draft) - at
}

func (u *appServerUI) wordBoundary(at int, backward bool) int {
	low, high := 0, len(u.draft)
	for _, image := range u.images {
		if backward && at == image.end {
			return image.start
		}
		if !backward && at == image.start {
			return image.end
		}
		if image.end <= at {
			low = image.end
		}
		if image.start >= at {
			high = image.start
			break
		}
	}
	if backward {
		for at > low {
			r, size := utf8.DecodeLastRuneInString(u.draft[:at])
			if !unicode.IsSpace(r) {
				break
			}
			at -= size
		}
		for at > low {
			r, size := utf8.DecodeLastRuneInString(u.draft[:at])
			if unicode.IsSpace(r) {
				break
			}
			at -= size
		}
	} else {
		for at < high {
			r, size := utf8.DecodeRuneInString(u.draft[at:])
			if unicode.IsSpace(r) {
				break
			}
			at += size
		}
		for at < high {
			r, size := utf8.DecodeRuneInString(u.draft[at:])
			if !unicode.IsSpace(r) {
				break
			}
			at += size
		}
	}
	return at
}
