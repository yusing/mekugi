package mekugi

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

const maxReviewComposeRows = 1 << 20

// ReviewComposition composes one file's ordered, applied review captures. It
// retains only captured lines, not source files. Its zero value is ready to use.
// Flush hides reviewed regions without discarding their original baseline.
type ReviewComposition struct {
	started               bool
	beforePath, afterPath string
	pathReviewed          bool
	pathHighlighted       bool
	regions               []reviewRegion
	original, current     map[int]string
}

type reviewRegion struct {
	beforeStart, afterStart int
	before, after           []string
	reviewed                bool
	highlighted             bool
}

// ReviewRow is a validated source row. Kind is ' ', '-', or '+' for context,
// removal, or addition. Text retains source bytes, including its line terminator;
// a missing final newline is represented by the absence of that terminator.
type ReviewRow struct {
	Kind byte
	Text string
}

// ReviewHunk is one validated captured hunk with zero-based source coordinates.
// ChangedStart excludes leading context; deletions use their anchor.
// Rows exposes source content without diff framing.
type ReviewHunk struct {
	BeforeStart, AfterStart int
	ChangedStart            int
	Rows                    []ReviewRow
}

// Hunks exposes captured geometry without reading files or duplicating the
// engine's diff parser in viewers. Path-only changes return no hunks.
func (file ReviewFile) Hunks() ([]ReviewHunk, error) {
	return parseReviewHunks(file, false)
}

type reviewEdit struct {
	start         int
	before, after []string
}

// Apply validates the captured chain before publishing any new composition.
// reviewed is true when the user already flushed this attempt while it was
// awaiting confirmation; a late receipt alone must not revive reviewed content.
func (c *ReviewComposition) Apply(file ReviewFile, reviewed bool) error {
	return c.ApplyWithHighlight(file, reviewed, false)
}

// ApplyWithHighlight also marks touched net regions for auxiliary display.
// Highlights follow source regions through composition, not rendered line numbers.
// Reviewed captures cannot introduce highlights; a full revert removes them.
func (c *ReviewComposition) ApplyWithHighlight(file ReviewFile, reviewed, highlighted bool) error {
	highlighted = highlighted && !reviewed
	hunks, err := parseReviewHunks(file, true)
	if err != nil {
		return err
	}
	next := *c
	next.regions = slices.Clone(c.regions)
	next.original, next.current = maps.Clone(c.original), maps.Clone(c.current)
	if !next.started {
		next.started = true
		next.beforePath, next.afterPath = file.BeforePath, file.BeforePath
		next.original, next.current = make(map[int]string), make(map[int]string)
	}
	if file.BeforePath != next.afterPath {
		return errors.New("captured file identity does not continue the composed chain")
	}
	// Validate and learn context before changing any coordinates.
	var edits []reviewEdit
	for _, hunk := range hunks {
		position := hunk.BeforeStart
		var edit *reviewEdit
		for _, row := range hunk.Rows {
			if row.Kind != '+' {
				if known, ok := next.current[position]; ok && known != row.Text {
					return errors.New("captured source does not match the composed chain")
				}
				next.current[position] = row.Text
				if original, ok := next.originalPosition(position); ok {
					if known, exists := next.original[original]; exists && known != row.Text {
						return errors.New("captured original context does not match the composed chain")
					}
					next.original[original] = row.Text
				}
			}
			if row.Kind == ' ' {
				edit = nil
			} else {
				if edit == nil {
					edits = append(edits, reviewEdit{start: position})
					edit = &edits[len(edits)-1]
				}
				if row.Kind == '-' {
					edit.before = append(edit.before, row.Text)
				} else {
					edit.after = append(edit.after, row.Text)
				}
			}
			if row.Kind != '+' {
				position++
			}
		}
	}
	// Descending source order keeps all input coordinates on one baseline.
	for _, edit := range slices.Backward(edits) {
		if err := next.applyEdit(edit, reviewed, highlighted); err != nil {
			return err
		}
	}
	next.normalize()
	if len(next.original) > maxReviewComposeRows || len(next.current) > maxReviewComposeRows {
		return errors.New("review composition exceeds captured-line capacity")
	}
	if file.BeforePath != file.AfterPath {
		next.pathReviewed = reviewed
		next.pathHighlighted = highlighted
	}
	next.afterPath = file.AfterPath
	*c = next
	return nil
}

func (c *ReviewComposition) originalPosition(position int) (int, bool) {
	shift := 0
	for _, region := range c.regions {
		if position < region.afterStart {
			break
		}
		if position < region.afterStart+len(region.after) {
			return 0, false
		}
		shift += len(region.after) - len(region.before)
	}
	return position - shift, true
}

func reviewOverlaps(start, end int, region reviewRegion) bool {
	a, b := region.afterStart, region.afterStart+len(region.after)
	if start == end && a == b {
		return start == a
	}
	if start == end {
		return a < start && start < b
	}
	if a == b {
		return start < a && a < end
	}
	return start < b && a < end
}

func (c *ReviewComposition) applyEdit(edit reviewEdit, reviewed, highlighted bool) error {
	start, end := edit.start, edit.start+len(edit.before)
	first, last := -1, -1
	for i, region := range c.regions {
		if reviewOverlaps(start, end, region) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	left, right := start, end
	if first >= 0 {
		left = min(left, c.regions[first].afterStart)
		right = max(right, c.regions[last].afterStart+len(c.regions[last].after))
	}
	if right-left > maxReviewComposeRows {
		return errors.New("review region exceeds capacity")
	}
	originalStart, ok := c.originalPosition(left)
	if first >= 0 && left == c.regions[first].afterStart {
		originalStart, ok = c.regions[first].beforeStart, true
	}
	if !ok {
		for _, region := range c.regions {
			if left == region.afterStart {
				originalStart, ok = region.beforeStart, true
				break
			}
		}
	}
	if !ok {
		return errors.New("review region has no original boundary")
	}
	current := make([]string, right-left)
	for i := range current {
		line, exists := c.current[left+i]
		if !exists {
			return errors.New("review composition requires uncaptured source")
		}
		current[i] = line
	}
	if !slices.Equal(current[start-left:end-left], edit.before) {
		return errors.New("captured edit does not match the composed chain")
	}
	original := slices.Clone(current)
	if first >= 0 {
		for i := last; i >= first; i-- {
			region := c.regions[i]
			offset := region.afterStart - left
			original = slices.Replace(original, offset, offset+len(region.after), region.before...)
		}
	}
	after := slices.Replace(slices.Clone(current), start-left, end-left, edit.after...)
	if len(original) > maxReviewComposeRows || len(after) > maxReviewComposeRows {
		return errors.New("review region exceeds capacity")
	}
	shift := len(edit.after) - len(edit.before)
	updated := make(map[int]string, len(c.current)+len(edit.after))
	for position, line := range c.current {
		switch {
		case position < start:
			updated[position] = line
		case position >= end:
			updated[position+shift] = line
		}
	}
	for i, line := range edit.after {
		updated[start+i] = line
	}
	c.current = updated
	// Never mutate slices retained by the previous, still-valid composition.
	var regions []reviewRegion
	for i, region := range c.regions {
		if first >= 0 && i >= first && i <= last {
			continue
		}
		if region.afterStart >= end {
			region.afterStart += shift
		}
		regions = append(regions, region)
	}
	if !slices.Equal(original, after) {
		visible := !reviewed
		if first >= 0 {
			for _, region := range c.regions[first : last+1] {
				visible = visible || !region.reviewed
				highlighted = highlighted || region.highlighted
			}
		}
		regions = append(regions, reviewRegion{beforeStart: originalStart, afterStart: left, before: original, after: after, reviewed: !visible, highlighted: highlighted})
	}
	slices.SortFunc(regions, func(a, b reviewRegion) int {
		if a.afterStart != b.afterStart {
			return a.afterStart - b.afterStart
		}
		return a.beforeStart - b.beforeStart
	})
	c.regions = regions
	return nil
}

// Repeated source lines can align a revert as an insertion beside a deletion.
// Normalize connected, fully captured regions together so equivalent net text
// cancels without merging the acknowledgement of unrelated changed runs.
func (c *ReviewComposition) normalize() {
	var normalized []reviewRegion
	for first := 0; first < len(c.regions); {
		initial := c.regions[first]
		a, b := slices.Clone(initial.before), slices.Clone(initial.after)
		last := first + 1
		for last < len(c.regions) {
			r := c.regions[last]
			oldEnd, newEnd := initial.beforeStart+len(a), initial.afterStart+len(b)
			gap := r.beforeStart - oldEnd
			if gap < 0 || gap != r.afterStart-newEnd {
				break
			}
			var context []string
			for i := 0; i < gap; i++ {
				oldLine, oldOK := c.original[oldEnd+i]
				newLine, newOK := c.current[newEnd+i]
				if !oldOK || !newOK || oldLine != newLine {
					break
				}
				context = append(context, oldLine)
			}
			if len(context) != gap {
				break
			}
			a = append(append(a, context...), r.before...)
			b = append(append(b, context...), r.after...)
			last++
		}
		for _, op := range difflib.NewMatcher(a, b).GetOpCodes() {
			if op.Tag == 'e' {
				continue
			}
			// Re-diff the whole connected group to cancel repeated-line reverts,
			// then preserve original region boundaries inside replacement opcodes.
			// A single opcode can otherwise revive an adjacent reviewed edit.
			oldStart, newStart := op.I1, op.J1
			emit := func(oldEnd, newEnd int) {
				if oldEnd < oldStart || newEnd < newStart ||
					oldEnd > op.I2 || newEnd > op.J2 {
					return
				}
				if oldEnd == oldStart && newEnd == newStart {
					return
				}
				region := reviewRegion{
					beforeStart: initial.beforeStart + oldStart, afterStart: initial.afterStart + newStart,
					before: slices.Clone(a[oldStart:oldEnd]), after: slices.Clone(b[newStart:newEnd]), reviewed: true,
				}
				oldStart, newStart = oldEnd, newEnd
				if slices.Equal(region.before, region.after) {
					return
				}
				for _, prior := range c.regions[first:last] {
					oldSpan := reviewRegion{afterStart: prior.beforeStart, after: prior.before}
					if !prior.reviewed && (len(region.after) > 0 && len(prior.after) > 0 &&
						reviewOverlaps(region.afterStart, region.afterStart+len(region.after), prior) ||
						len(region.before) > 0 && len(prior.before) > 0 &&
							reviewOverlaps(region.beforeStart, region.beforeStart+len(region.before), oldSpan)) {
						region.reviewed = false
						region.highlighted = region.highlighted || prior.highlighted
					}
				}
				normalized = append(normalized, region)
			}
			for _, prior := range c.regions[first:last] {
				emit(prior.beforeStart-initial.beforeStart, prior.afterStart-initial.afterStart)
				emit(prior.beforeStart+len(prior.before)-initial.beforeStart, prior.afterStart+len(prior.after)-initial.afterStart)
			}
			emit(op.I2, op.J2)
		}
		first = last
	}
	c.regions = normalized
}

// Flush acknowledges every current region, including path-only changes.
// It does not change the baseline or mutate any durable capture.
func (c *ReviewComposition) Flush() {
	for i := range c.regions {
		c.regions[i].reviewed = true
		c.regions[i].highlighted = false
	}
	c.pathReviewed = true
	c.pathHighlighted = false
}

// Files returns only unreviewed original-to-latest regions. Matching captured
// context is included when available; unknown source is never synthesized.
func (c *ReviewComposition) Files() []ReviewFile {
	var files []ReviewFile
	for _, file := range c.FilesWithHighlights() {
		files = append(files, file.ReviewFile)
	}
	return files
}

// ReviewHighlightedFile pairs a composed projection with process-local display
// metadata. Highlights are not part of immutable captured ReviewFile records.
type ReviewHighlightedFile struct {
	ReviewFile
	Highlighted bool
}

// FilesWithHighlights returns the same visible regions as Files, with highlights
// introduced by ApplyWithHighlight. Path-only changes mark their header projection.
func (c *ReviewComposition) FilesWithHighlights() []ReviewHighlightedFile {
	var files []ReviewHighlightedFile
	for _, region := range c.regions {
		if region.reviewed {
			continue
		}
		a, b := slices.Clone(region.before), slices.Clone(region.after)
		oldStart, newStart := region.beforeStart, region.afterStart
		for range 3 {
			left, ok1 := c.original[oldStart-1]
			right, ok2 := c.current[newStart-1]
			if oldStart == 0 || newStart == 0 || !ok1 || !ok2 || left != right {
				break
			}
			a, b = slices.Insert(a, 0, left), slices.Insert(b, 0, right)
			oldStart--
			newStart--
		}
		for range 3 {
			left, ok1 := c.original[oldStart+len(a)]
			right, ok2 := c.current[newStart+len(b)]
			if !ok1 || !ok2 || left != right {
				break
			}
			a, b = append(a, left), append(b, right)
		}
		files = append(files, ReviewHighlightedFile{
			ReviewFile:  renderReviewFile(ReviewFile{BeforePath: c.beforePath, AfterPath: c.afterPath}, a, b, oldStart, newStart),
			Highlighted: region.highlighted,
		})
	}
	if len(files) == 0 && c.started && c.beforePath != c.afterPath && !c.pathReviewed {
		files = append(files, ReviewHighlightedFile{
			ReviewFile:  renderReviewFile(ReviewFile{BeforePath: c.beforePath, AfterPath: c.afterPath}, nil, nil, 0, 0),
			Highlighted: c.pathHighlighted,
		})
	}
	return files
}

// Parse the engine's review format, retaining exact source line endings. No
// filesystem lookup, fuzzy application, or executor-patch semantics are involved.
func parseReviewHunks(file ReviewFile, complete bool) ([]ReviewHunk, error) {
	lines := strings.SplitAfter(file.UnifiedDiff(), "\n")
	var hunks []ReviewHunk
	previousEnd, previousAfterEnd, shift, rowCount := 0, 0, 0, 0
	for i := 0; i < len(lines); {
		line := lines[i]
		i++
		if !strings.HasPrefix(line, "@@ ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 || fields[0] != "@@" || fields[3] != "@@" {
			return nil, errors.New("invalid captured hunk header")
		}
		oldStart, oldCount, err := parseReviewRange(fields[1], '-')
		if err != nil {
			return nil, err
		}
		newStart, newCount, err := parseReviewRange(fields[2], '+')
		if err != nil {
			return nil, err
		}
		if oldStart < previousEnd || newStart < previousAfterEnd || complete && newStart != oldStart+shift {
			return nil, errors.New("inconsistent captured hunk coordinates")
		}
		var body []ReviewRow
		oldRows, newRows := 0, 0
		for i < len(lines) && !strings.HasPrefix(lines[i], "@@ ") {
			text := lines[i]
			i++
			if text == "" && i == len(lines) {
				break
			}
			if text == "\\ No newline at end of file\n" {
				if len(body) == 0 || !strings.HasSuffix(body[len(body)-1].Text, "\n") {
					return nil, errors.New("invalid captured newline marker")
				}
				body[len(body)-1].Text = strings.TrimSuffix(body[len(body)-1].Text, "\n")
				continue
			}
			if len(text) == 0 || !strings.ContainsRune(" +-", rune(text[0])) {
				return nil, errors.New("invalid captured hunk row")
			}
			body = append(body, ReviewRow{Kind: text[0], Text: text[1:]})
			if text[0] != '+' {
				oldRows++
			}
			if text[0] != '-' {
				newRows++
			}
			rowCount++
			if rowCount > maxReviewComposeRows {
				return nil, errors.New("captured hunk exceeds capacity")
			}
		}
		if oldRows != oldCount || newRows != newCount {
			return nil, errors.New("captured hunk counts do not match content")
		}
		changed := newStart
		if first := slices.IndexFunc(body, func(row ReviewRow) bool { return row.Kind != ' ' }); first >= 0 {
			changed += first
		}
		hunks = append(hunks, ReviewHunk{
			BeforeStart: oldStart, AfterStart: newStart, ChangedStart: changed, Rows: body,
		})
		previousAfterEnd = newStart + newCount
		previousEnd, shift = oldStart+oldCount, shift+newCount-oldCount
	}
	return hunks, nil
}

func parseReviewRange(text string, prefix byte) (int, int, error) {
	if len(text) < 2 || text[0] != prefix {
		return 0, 0, errors.New("invalid captured hunk range")
	}
	startText, countText, explicit := strings.Cut(text[1:], ",")
	start, err := strconv.Atoi(startText)
	if err != nil || start < 0 || start > 1<<30 {
		return 0, 0, fmt.Errorf("invalid captured hunk position %q", startText)
	}
	count := 1
	if explicit {
		count, err = strconv.Atoi(countText)
		if err != nil || count < 0 || count > maxReviewComposeRows {
			return 0, 0, errors.New("invalid captured hunk length")
		}
	}
	if count != 0 {
		start--
	}
	if start < 0 {
		return 0, 0, errors.New("invalid captured hunk start")
	}
	return start, count, nil
}
