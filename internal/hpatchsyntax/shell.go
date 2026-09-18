package hpatchsyntax

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ScriptSegment is one atomic edit script or one shell program. Line is
// the one-based physical start in the complete input.
type ScriptSegment struct {
	Source string
	Line   int
	Shell  bool
}

// SplitShell separates routed shell commands without interpreting shell source or
// recognizing frame markers inside edit values. The bool distinguishes ordinary
// HPATCH, whose existing parser continues to own all diagnostics.
func SplitShell(source string) ([]ScriptSegment, bool, error) {
	lines := SplitPhysicalLines(source)
	var segments []ScriptSegment
	start, offset, startOffset := 0, 0, 0
	mixed := false
	appendEdit := func(end int) {
		if strings.TrimSpace(source[startOffset:end]) != "" {
			segments = append(segments, ScriptSegment{Source: source[startOffset:end], Line: start + 1})
		}
	}
	for index := 0; index < len(lines); {
		line := lines[index]
		if line.Text == "shell" || strings.HasPrefix(line.Text, "shell ") {
			mixed = true
			appendEdit(offset)
			header := index
			var body string
			if strings.HasPrefix(line.Text, "shell <<") {
				frame, err := FrameCommand(lines, index, line.Text)
				if err != nil {
					return nil, true, fmt.Errorf("line %d: %w", header+1, err)
				}
				body = frame.Body
				for index < frame.Next {
					offset += len(lines[index].Text) + len(lines[index].Terminator)
					index++
				}
			} else {
				body = strings.TrimPrefix(line.Text, "shell ")
				if strings.Contains(body, "<<") {
					return nil, true, fmt.Errorf("line %d: << is not allowed in a single-line shell command; use a heredoc", header+1)
				}
				if line.Text == "shell" {
					body = ""
				}
				offset += len(line.Text) + len(line.Terminator)
				index++
			}
			if len(body) > MaxHeredocBodyBytes || !utf8.ValidString(body) {
				return nil, true, fmt.Errorf("line %d: shell body must be UTF-8 within %d bytes", header+1, MaxHeredocBodyBytes)
			}
			segments = append(segments, ScriptSegment{Source: body, Line: header + 1, Shell: true})
			start, startOffset = index, offset
			continue
		}
		frame, err := FrameCommand(lines, index, line.Text)
		if err != nil {
			return nil, mixed, fmt.Errorf("line %d: %w", index+1, err)
		}
		for index < frame.Next {
			offset += len(lines[index].Text) + len(lines[index].Terminator)
			index++
		}
	}
	appendEdit(offset)
	return segments, mixed, nil
}
