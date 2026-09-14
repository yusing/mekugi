package mekugi

import (
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// ReviewFile is an immutable original-to-final review projection. Unlike the
// executor patch it includes deleted content and preserves line-ending changes.
// Paths are empty on the absent side of an addition or deletion.
type ReviewFile struct {
	BeforePath string
	AfterPath  string
	Diff       string
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
func (file ReviewFile) LineCounts() (added, removed int) {
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

// Summary describes one evaluated file change, not a net diff across calls.
func (file ReviewFile) Summary() string {
	action, path := "update", fmt.Sprintf("%q", file.AfterPath)
	switch {
	case file.BeforePath == "":
		action = "add"
	case file.AfterPath == "":
		action, path = "delete", fmt.Sprintf("%q", file.BeforePath)
	case file.BeforePath != file.AfterPath:
		action, path = "move", fmt.Sprintf("%q -> %q", file.BeforePath, file.AfterPath)
	}
	added, removed := file.LineCounts()
	return fmt.Sprintf("%s %s +%d -%d\n", action, path, added, removed)
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

// renderReviewFile also renders sparse composed regions, without pretending that
// uncaptured source outside a region is known.
func renderReviewFile(file ReviewFile, a, b []string, beforeOffset, afterOffset int) ReviewFile {
	action := "update"
	switch {
	case file.BeforePath == "":
		action = "add"
	case file.AfterPath == "":
		action = "delete"
	case file.BeforePath != file.AfterPath:
		action = "move"
	}
	var diff strings.Builder
	fmt.Fprintf(&diff, "%s %q -> %q\n", action, file.BeforePath, file.AfterPath)
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
