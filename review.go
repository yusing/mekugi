package mekugi

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/pmezard/go-difflib/difflib"
)

// ReviewFile is an immutable original-to-final review projection. Unlike the
// executor patch it includes deleted content and preserves line-ending changes.
// Paths are empty on the absent side of an addition or deletion.
type ReviewFile struct {
	BeforePath string
	AfterPath  string
	Diff       string
	// Incomplete describes unavailable content; it is not an empty-file diff.
	Incomplete string `json:",omitzero"`
}

// ReviewAction classifies a committed before/after file identity. Renderers use
// this one classification instead of inferring operation labels independently.
type ReviewAction string

const (
	ReviewAdd    ReviewAction = "add"
	ReviewUpdate ReviewAction = "update"
	ReviewDelete ReviewAction = "delete"
	ReviewMove   ReviewAction = "move"
)

// Action classifies the committed file operation from its captured identities.
func (file ReviewFile) Action() ReviewAction {
	switch {
	case file.BeforePath == "":
		return ReviewAdd
	case file.AfterPath == "":
		return ReviewDelete
	case file.BeforePath != file.AfterPath:
		return ReviewMove
	default:
		return ReviewUpdate
	}
}

// Title returns the user-facing operation name for reports and commentary.
func (action ReviewAction) Title() string {
	switch action {
	case ReviewAdd:
		return "Create"
	case ReviewDelete:
		return "Delete"
	case ReviewMove:
		return "Move"
	default:
		return "Edit"
	}
}

// UnifiedDiff omits the redundant operation header when unified headers already
// describe the file. Header-only changes (empty files and pure moves) retain it.
// Rendering captured records here also keeps historical reads consistent.
func (file ReviewFile) UnifiedDiff() string {
	_, rest, ok := strings.Cut(file.Diff, "\n")
	header := fmt.Sprintf("--- %s\n+++ %s\n", reviewPath(file.BeforePath), reviewPath(file.AfterPath))
	if ok && strings.HasPrefix(rest, header) {
		return rest
	}
	return file.Diff
}

// LineCounts counts added and removed source rows in a captured review projection.
// File headers, context, and missing-final-newline markers are not source changes.
// Incomplete captures return -1 for both counts.
func (file ReviewFile) LineCounts() (added, removed int) {
	if file.Incomplete != "" {
		return -1, -1 // Unknown, not zero changed rows.
	}
	inHunk := false
	for line := range strings.SplitSeq(file.Diff, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			inHunk = true
		} else if inHunk && strings.HasPrefix(line, "+") {
			added++
		} else if inHunk && strings.HasPrefix(line, "-") {
			removed++
		}
	}
	return added, removed
}

// ReviewStat renders a diffstat for one evaluation, not a net diff across calls.
// Bars share a scale and are capped at 40 characters; totals remain exact.
func ReviewStat(files []ReviewFile) string {
	if len(files) == 0 {
		return ""
	}
	var complete []ReviewFile
	var unavailable strings.Builder
	for _, file := range files {
		if file.Incomplete == "" {
			complete = append(complete, file)
		} else {
			path := file.AfterPath
			if path == "" {
				path = file.BeforePath
			}
			fmt.Fprintf(&unavailable, " %q | unavailable (incomplete history: %s)\n", path, file.Incomplete)
		}
	}
	if unavailable.Len() != 0 {
		return ReviewStat(complete) + unavailable.String()
	}
	type entry struct {
		path           string
		added, removed int
	}
	entries := make([]entry, 0, len(files))
	pathWidth, largest, added, removed := 0, 0, 0, 0
	displayPath := func(path string) string {
		if strings.IndexFunc(path, unicode.IsControl) >= 0 {
			return strconv.Quote(path)
		}
		return path
	}
	for _, file := range files {
		path := displayPath(file.AfterPath)
		switch {
		case file.AfterPath == "":
			path = displayPath(file.BeforePath)
		case file.BeforePath != "" && file.BeforePath != file.AfterPath:
			path = displayPath(file.BeforePath) + " => " + path
		}
		a, r := file.LineCounts()
		entries = append(entries, entry{path, a, r})
		pathWidth = max(pathWidth, ansi.StringWidth(path))
		largest = max(largest, a+r)
		added += a
		removed += r
	}
	var output strings.Builder
	countWidth := len(strconv.Itoa(largest))
	for _, entry := range entries {
		a, r := entry.added, entry.removed
		if largest > 40 {
			width := max(1, (a+r)*40/largest)
			if a > 0 && r > 0 {
				width = max(2, width)
				a = max(1, min(width-1, a*width/(a+r)))
				r = width - a
			} else if a > 0 {
				a = width
			} else if r > 0 {
				r = width
			}
		}
		fmt.Fprintf(&output, " %s%s | %*d", entry.path, strings.Repeat(" ", pathWidth-ansi.StringWidth(entry.path)), countWidth, entry.added+entry.removed)
		if a+r > 0 {
			fmt.Fprintf(&output, " %s%s", strings.Repeat("+", a), strings.Repeat("-", r))
		}
		output.WriteByte('\n')
	}
	plural := func(count int) string {
		if count == 1 {
			return ""
		}
		return "s"
	}
	fmt.Fprintf(&output, " %d file%s changed", len(files), plural(len(files)))
	if added > 0 {
		fmt.Fprintf(&output, ", %d insertion%s(+)", added, plural(added))
	}
	if removed > 0 {
		fmt.Fprintf(&output, ", %d deletion%s(-)", removed, plural(removed))
	}
	output.WriteByte('\n')
	return output.String()
}

func reviewFiles(changes []change) []ReviewFile {
	files := make([]ReviewFile, 0, len(changes))
	for _, change := range changes {
		file := ReviewFile{BeforePath: change.originalPath, AfterPath: change.path}
		before, after := change.original, change.content
		switch change.kind {
		case changeAdd:
			file.BeforePath, before = "", ""
		case changeDelete:
			file.AfterPath, after = "", ""
		}
		files = append(files, renderReviewFile(file, reviewLines(before), reviewLines(after), 0, 0))
	}
	return files
}

// RenderReviewFile captures an operation-owned before/after pair using the same
// diff semantics as engine edits. An empty path denotes an absent side. Pure
// moves may supply empty contents on both sides without reading the source.
func RenderReviewFile(beforePath, afterPath, before, after string) ReviewFile {
	return renderReviewFile(ReviewFile{BeforePath: beforePath, AfterPath: afterPath}, reviewLines(before), reviewLines(after), 0, 0)
}

// RenderIncompleteReviewFile retains an applied operation's identity without
// inventing source rows when its contents could not be captured.
func RenderIncompleteReviewFile(beforePath, afterPath, reason string) ReviewFile {
	file := RenderReviewFile(beforePath, afterPath, "", "")
	file.Incomplete = reason
	file.Diff += "incomplete history: " + reason + "\n"
	return file
}

// renderReviewFile also renders sparse composed regions, without pretending that
// uncaptured source outside a region is known.
func renderReviewFile(file ReviewFile, a, b []string, beforeOffset, afterOffset int) ReviewFile {
	var diff strings.Builder
	fmt.Fprintf(&diff, "%s %q -> %q\n", file.Action(), file.BeforePath, file.AfterPath)
	groups := difflib.NewMatcher(a, b).GetGroupedOpCodes(3)
	if len(groups) != 0 {
		fmt.Fprintf(&diff, "--- %s\n+++ %s\n", reviewPath(file.BeforePath), reviewPath(file.AfterPath))
	}
	for _, group := range groups {
		first, last := group[0], group[len(group)-1]
		fmt.Fprintf(&diff, "@@ -%s +%s @@\n",
			reviewRange(beforeOffset+first.I1, beforeOffset+last.I2),
			reviewRange(afterOffset+first.J1, afterOffset+last.J2))
		for _, op := range group {
			if op.Tag == 'e' {
				writeReviewLines(&diff, ' ', a[op.I1:op.I2])
			}
			if op.Tag == 'r' || op.Tag == 'd' {
				writeReviewLines(&diff, '-', a[op.I1:op.I2])
			}
			if op.Tag == 'r' || op.Tag == 'i' {
				writeReviewLines(&diff, '+', b[op.J1:op.J2])
			}
		}
	}
	file.Diff = diff.String()
	return file
}

func reviewPath(path string) string {
	if path == "" {
		return "/dev/null"
	}
	return fmt.Sprintf("%q", path)
}

func reviewRange(start, end int) string {
	if start == end {
		return fmt.Sprintf("%d,0", start)
	}
	return fmt.Sprintf("%d,%d", start+1, end-start)
}

func reviewLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func writeReviewLines(output *strings.Builder, prefix byte, lines []string) {
	for _, line := range lines {
		output.WriteByte(prefix)
		output.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			output.WriteString("\n\\ No newline at end of file\n")
		}
	}
}
