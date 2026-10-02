package mekugi

import (
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/yusing/mekugi/internal/pathdisplay"
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
	// Directory is a metadata-only directory change. Its contents and descendant
	// paths are not retained and cannot be composed, reverted or reapplied.
	Directory bool `json:",omitzero"`
	// Binary marks intact non-text content, reviewed by size and hash only.
	Binary bool `json:",omitzero"`
	// Link marks a symbolic link on either side; its rows are link targets,
	// not file content.
	Link bool `json:",omitzero"`
	// CopyFrom names an unchanged source whose content an added file copies.
	CopyFrom string `json:",omitzero"`
	// Origin labels the tool that chose this file or its content. Empty means
	// the agent chose both.
	Origin     string `json:",omitzero"`
	OriginNote string `json:",omitzero"`
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

// UnifiedDiffForWorkspace shortens display headers without changing retained
// paths or source rows. Composition and filesystem operations use the originals.
func (file ReviewFile) UnifiedDiffForWorkspace(workspace string) string {
	before, after := file.BeforePath, file.AfterPath
	file.BeforePath = pathdisplay.ForWorkspace(workspace, before)
	file.AfterPath = pathdisplay.ForWorkspace(workspace, after)
	operation := fmt.Sprintf("%s %q -> %q\n", file.Action(), before, after)
	if rest, ok := strings.CutPrefix(file.Diff, operation); ok {
		file.Diff = fmt.Sprintf("%s %q -> %q\n", file.Action(), file.BeforePath, file.AfterPath) + rest
	}
	oldHeader := fmt.Sprintf("--- %s\n+++ %s\n", reviewPath(before), reviewPath(after))
	newHeader := fmt.Sprintf("--- %s\n+++ %s\n", reviewPath(file.BeforePath), reviewPath(file.AfterPath))
	// Headers occur at the start or immediately after the operation, never in hunks.
	first, rest, _ := strings.Cut(file.Diff, "\n")
	if file.Binary {
		oldBinary := fmt.Sprintf("Binary files %s and %s differ (", reviewPath(before), reviewPath(after))
		if tail, ok := strings.CutPrefix(rest, oldBinary); ok {
			file.Diff = first + "\n" + fmt.Sprintf("Binary files %s and %s differ (", reviewPath(file.BeforePath), reviewPath(file.AfterPath)) + tail
		}
	}
	if tail, ok := strings.CutPrefix(rest, oldHeader); ok {
		file.Diff = first + "\n" + newHeader + tail
	} else if tail, ok := strings.CutPrefix(file.Diff, oldHeader); ok {
		file.Diff = newHeader + tail
	}
	return file.UnifiedDiff()
}

// LineCounts counts added and removed source rows in a captured review projection.
// File headers, context, and missing-final-newline markers are not source changes.
// Incomplete captures return -1 for both counts.
func (file ReviewFile) LineCounts() (added, removed int) {
	if file.Incomplete != "" || file.Directory {
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

// RenderReviewFile captures an operation-owned before/after pair using the same
// diff semantics as engine edits. An empty path denotes an absent side. Pure
// moves may supply empty contents on both sides without reading the source.
func RenderReviewFile(beforePath, afterPath, before, after string) ReviewFile {
	return renderReviewFile(ReviewFile{BeforePath: beforePath, AfterPath: afterPath}, reviewLines(before), reviewLines(after), 0, 0)
}

// RenderBinaryReviewFile reviews intact non-text content without source rows,
// in the spirit of git's "Binary files differ".
func RenderBinaryReviewFile(beforePath, afterPath string, beforeSize, afterSize int64, beforeHash, afterHash string) ReviewFile {
	file := RenderReviewFile(beforePath, afterPath, "", "")
	file.Binary = true
	side := func(path string, size int64, hash string) string {
		if path == "" {
			return "absent"
		}
		if len(hash) > 12 {
			hash = hash[:12]
		}
		return fmt.Sprintf("%d bytes, sha256 %s", size, hash)
	}
	file.Diff += fmt.Sprintf("Binary files %s and %s differ (%s -> %s)\n",
		reviewPath(beforePath), reviewPath(afterPath), side(beforePath, beforeSize, beforeHash), side(afterPath, afterSize, afterHash))
	return file
}

// RenderIncompleteReviewFile retains an applied operation's identity without
// inventing source rows when its contents could not be captured.
func RenderIncompleteReviewFile(beforePath, afterPath, reason string) ReviewFile {
	file := RenderReviewFile(beforePath, afterPath, "", "")
	file.Incomplete = reason
	file.Diff += "incomplete history: " + reason + "\n"
	return file
}

// RenderUnbasedReviewFile shows a file's current content when its prior
// content is unknown. The rows are context for review, not a diff against an
// empty file, so line counts stay unavailable.
func RenderUnbasedReviewFile(path, after, reason string) ReviewFile {
	file := renderReviewFile(ReviewFile{BeforePath: path, AfterPath: path}, nil, reviewLines(after), 0, 0)
	file.Incomplete = reason
	file.Diff += "incomplete history: " + reason + "\n"
	return file
}

// renderReviewFile also renders sparse composed regions, without pretending that
// uncaptured source outside a region is known.
func renderReviewFile(file ReviewFile, a, b []string, beforeOffset, afterOffset int) ReviewFile {
	return renderReviewGroups(file, a, b, beforeOffset, afterOffset, difflib.NewMatcher(a, b).GetGroupedOpCodes(3))
}

func renderReviewGroups(file ReviewFile, a, b []string, beforeOffset, afterOffset int, groups [][]difflib.OpCode) ReviewFile {
	var diff strings.Builder
	fmt.Fprintf(&diff, "%s %q -> %q\n", file.Action(), file.BeforePath, file.AfterPath)
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
