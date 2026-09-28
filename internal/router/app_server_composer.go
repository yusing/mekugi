package router

import (
	"iter"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
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
	u.setNotice("", false)
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
	u.insertDraftText(text)
}

// Insert into an already-recorded edit, such as one picker replacement.
func (u *appServerUI) insertDraftText(text string) {
	at := u.cursor()
	u.draft = u.draft[:at] + text + u.draft[at:]
	for i := range u.files {
		if u.files[i].start >= at {
			u.files[i].start += len(text)
			u.files[i].end += len(text)
		}
	}
	for i := range u.skills {
		if u.skills[i].start >= at {
			u.skills[i].start += len(text)
			u.skills[i].end += len(text)
		}
	}
	for i := range u.images {
		if u.images[i].start >= at {
			u.images[i].start += len(text)
			u.images[i].end += len(text)
		}
	}
}

// displaySpans is the shared editing and presentation boundary for every
// atomic composer token. Payload and lifetime remain owned by each token kind.
func (d composerDraft) displaySpans() []activityui.TextSpan {
	var spans []activityui.TextSpan
	for _, token := range d.images {
		spans = append(spans, activityui.TextSpan{Start: token.start, End: token.end, Kind: activityui.ImageToken})
	}
	for _, token := range d.skills {
		spans = append(spans, activityui.TextSpan{Start: token.start, End: token.end, Kind: activityui.SkillToken})
	}
	for _, token := range d.files {
		spans = append(spans, activityui.TextSpan{Start: token.start, End: token.end, Kind: activityui.FileToken})
	}
	slices.SortFunc(spans, func(a, b activityui.TextSpan) int { return a.Start - b.Start })
	return spans
}

func (u *appServerUI) draftTokenSpans() iter.Seq2[int, int] {
	return func(yield func(int, int) bool) {
		for _, token := range (composerDraft{images: u.images, skills: u.skills, files: u.files}).displaySpans() {
			if !yield(token.Start, token.End) {
				return
			}
		}
	}
}

func (u *appServerUI) draftBoundary(at, direction int) int {
	for start, end := range u.draftTokenSpans() {
		if direction < 0 && at > start && at <= end {
			return start
		}
		if direction > 0 && at >= start && at < end {
			return end
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
	u.setNotice("", false)
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
	keptSkills := u.skills[:0]
	for _, skill := range u.skills {
		if skill.start < end && skill.end > start {
			continue
		}
		if skill.start >= end {
			skill.start -= end - start
			skill.end -= end - start
		}
		keptSkills = append(keptSkills, skill)
	}
	u.skills = keptSkills
	keptFiles := u.files[:0]
	for _, file := range u.files {
		if file.start < end && file.end > start {
			continue
		}
		if file.start >= end {
			file.start -= end - start
			file.end -= end - start
		}
		keptFiles = append(keptFiles, file)
	}
	u.files = keptFiles
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
		for start, end := range u.draftTokenSpans() {
			boundaries = append(boundaries, start, end)
		}
		boundaries = append(boundaries, len(u.draft))
		slices.Sort(boundaries)
		boundaries = slices.Compact(boundaries)
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

// Layout and navigation share the same atomic-span layout as submitted input.
func (u *appServerUI) draftLayout() ([]string, []activityui.TextPoint) {
	width := u.composerWidth
	if width == 0 {
		width = 80
	}
	rows, points := activityui.LayoutSpans(u.draft, (composerDraft{images: u.images, skills: u.skills, files: u.files}).displaySpans(), width)
	last := points[len(points)-1]
	if width > 1 && last.Column >= width {
		rows = append(rows, "")
		points[len(points)-1] = activityui.TextPoint{Offset: len(u.draft), Row: len(rows) - 1}
	}
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
	case "\x1b[1;5D", "\x1b[1;5C", "\x1b[1;3D", "\x1b[1;3C", "\x1bb", "\x1bf":
		at = u.wordBoundary(at, sequence == "\x1bb" || strings.HasSuffix(sequence, "D"))
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
			if point.Offset == at {
				current = point
				break
			}
		}
		if u.cursorColumn == nil {
			u.cursorColumn = new(current.Column)
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
			if point.Column <= *u.cursorColumn {
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
	for start, end := range u.draftTokenSpans() {
		if at > start && at < end {
			if at > u.cursor() {
				at = end
			} else {
				at = start
			}
			break
		}
	}
	u.cursorBack = len(u.draft) - at
}

func (u *appServerUI) wordBoundary(at int, backward bool) int {
	low, high := 0, len(u.draft)
	for start, end := range u.draftTokenSpans() {
		if backward && at == end {
			return start
		}
		if !backward && at == start {
			return end
		}
		if end <= at {
			low = max(low, end)
		}
		if start >= at {
			high = min(high, start)
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
