package mekugi

import (
	"fmt"
	"strings"
)

// boundaryAdvisory describes authored splices against the immutable baseline,
// not the final file: neighboring commands and language formatting may change
// those boundaries later. It never participates in mutation or validation.
func (e *editor) boundaryAdvisory(origin editOrigin, command instruction) string {
	var deleted, removed, blankBefore, blankAfter, joinedLeft int
	for _, edit := range e.edits {
		if edit.command != origin.command {
			continue
		}
		target := e.baseline[edit.start:edit.end]
		if command.operation == "type" {
			if command.text == "" && edit.start != edit.end {
				deleted++
			}
			if lineTerminatorSuffix(target) != "" && lineTerminatorSuffix(command.text) == "" {
				if command.text == "" || (command.target.kind != targetLine && command.target.kind != targetRange) {
					removed++
				}
			}
		}
		left, right := e.baseline[:edit.start], e.baseline[edit.end:]
		if blankBoundary(left, edit.replacement) {
			blankBefore++
		}
		if blankBoundary(edit.replacement, right) {
			blankAfter++
		}
		if command.operation == "add" && command.target.kind == targetEOF &&
			left != "" && lineTerminatorSuffix(left) == "" &&
			edit.replacement != "" && edit.replacement[0] != '\r' && edit.replacement[0] != '\n' {
			joinedLeft++
		}
	}
	if deleted+removed+blankBefore+blankAfter+joinedLeft == 0 {
		return ""
	}

	var report strings.Builder
	for _, count := range []struct {
		name string
		n    int
	}{
		{"deletes", deleted},
		{"removes-ending", removed},
		{"blank-before", blankBefore},
		{"blank-after", blankAfter},
		{"joins-left", joinedLeft},
	} {
		if count.n != 0 {
			if report.Len() != 0 {
				report.WriteByte(' ')
			}
			fmt.Fprintf(&report, "%s=%d", count.name, count.n)
		}
	}
	return report.String()
}

// blankBoundary identifies a blank (possibly space/tab-only) line where a
// terminating left side meets the right side. A split CRLF is one terminator,
// not an empty line. Only the adjacent line is inspected.
func blankBoundary(left, right string) bool {
	ending := lineTerminatorSuffix(left)
	if ending == "" || right == "" || (ending == "\r" && right[0] == '\n') {
		return false
	}
	tail := strings.TrimLeft(right, " \t")
	return strings.HasPrefix(tail, "\r") || strings.HasPrefix(tail, "\n")
}
