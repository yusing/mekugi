package mekugi

import (
	"fmt"
	"strconv"
	"strings"
)

const repairLineWindow = 2

const (
	repairPreviewLimit = 200
	repairListLimit    = 16
)

func (w *workspace) repairContext(command instruction, reason failureReason) string {
	file := w.paths[command.path]
	if file == nil || file.editor.baseline == "" || command.target.kind == targetNone {
		return ""
	}
	editor := &file.editor
	lines := logicalLines(editor.baseline)
	if len(lines) == 0 {
		return ""
	}

	var report strings.Builder
	switch reason {
	case reasonRowStale:
		writeStaleTargetRepair(&report, editor.baseline, lines, command.target)
	case reasonOccurrenceMissing:
		writeTextTargetRepair(&report, editor, lines, command.target)
	case reasonTargetOrder:
		fmt.Fprintf(&report, "row range resolves to lines %d:%d\n", command.target.start.line, command.target.end.line)
		writeLineWindow(&report, editor.baseline, lines, command.target.start.line)
	case reasonEditConflict:
		report.WriteString("baseline content conflicts with an earlier mutation\n")
		if claimed := editor.claimedLineSpans(lines); claimed != "" {
			fmt.Fprintf(&report, "earlier mutations: %s\n", claimed)
		}
		writeLineWindow(&report, editor.baseline, lines, command.target.start.line)
	}
	return report.String()
}

func writeStaleTargetRepair(report *strings.Builder, baseline string, lines []logicalLine, target targetSpec) {
	writeRangeCandidateRepair(report, baseline, lines, target)
	references := []struct {
		name string
		row  rowReference
	}{{name: "target", row: target.start}}
	if target.kind == targetRange {
		references[0].name = "range start"
		references = append(references, struct {
			name string
			row  rowReference
		}{name: "range end", row: target.end})
	}
	for _, reference := range references {
		if reference.row.line < 1 || reference.row.line > len(lines) {
			fmt.Fprintf(report, "%s row %d is absent from the current baseline\n", reference.name, reference.row.line)
			continue
		}
		current := lineContent(baseline, lines[reference.row.line-1])
		actual := hashLine(current)
		if actual == reference.row.hash {
			fmt.Fprintf(report, "%s verified at %d:%s\n", reference.name, reference.row.line, reference.row.hash)
			continue
		}
		fmt.Fprintf(
			report,
			"%s expected %d:%s; current-line candidate (verify text): %d:%s\n",
			reference.name,
			reference.row.line,
			reference.row.hash,
			reference.row.line,
			actual,
		)
		var relocated []int
		for index, line := range lines {
			if index+1 != reference.row.line && hashLine(lineContent(baseline, line)) == reference.row.hash {
				relocated = append(relocated, index+1)
			}
		}
		if len(relocated) != 0 {
			fmt.Fprintf(report, "%s hash also occurs at lines %s; verify whether the target moved\n", reference.name, joinLineNumbers(relocated[:min(len(relocated), repairListLimit)], len(relocated)))
		} else {
			fmt.Fprintf(report, "%s hash is absent elsewhere; reread before choosing a replacement target\n", reference.name)
		}
		writeLineWindow(report, baseline, lines, reference.row.line)
	}
}

func writeRangeCandidateRepair(report *strings.Builder, baseline string, lines []logicalLine, target targetSpec) {
	if target.kind != targetRange ||
		target.start.line < 1 || target.end.line > len(lines) ||
		target.start.line > target.end.line {
		return
	}
	start := hashLine(lineContent(baseline, lines[target.start.line-1]))
	end := hashLine(lineContent(baseline, lines[target.end.line-1]))
	fmt.Fprintf(
		report,
		"range candidate at requested coordinates (verify both endpoint texts and complete %d-line span): %d:%s..%d:%s\n",
		target.end.line-target.start.line+1,
		target.start.line,
		start,
		target.end.line,
		end,
	)
}

func writeTextTargetRepair(report *strings.Builder, editor *editor, lines []logicalLine, target targetSpec) {
	search := editor.baseline
	anchorOffset := 0
	location := "in immutable baseline"
	if target.kind == targetText {
		anchor, err := resolveRow(editor.baseline, target.start)
		if err != nil {
			return
		}
		anchorOffset = anchor.Start
		search = editor.baseline[anchor.Start:]
		location = fmt.Sprintf("at or after line %d", target.start.line)
	}
	offsets := nonOverlappingLiteralOffsets(search, target.literal, target.count)
	fmt.Fprintf(report, "found %d of %d requested matches %s\n", len(offsets), target.count, location)
	report.WriteString("if an earlier mutation introduces the target, apply that prerequisite, reread, and submit a later invocation\n")
	if len(offsets) != 0 {
		matchLines := make([]int, 0, min(len(offsets), repairListLimit))
		for _, offset := range offsets[:min(len(offsets), repairListLimit)] {
			matchLines = append(matchLines, lineNumberAt(lines, anchorOffset+offset))
		}
		fmt.Fprintf(report, "matching lines: %s\n", joinLineNumbers(matchLines, len(offsets)))
	}
	if target.kind == targetText {
		writeLineWindow(report, editor.baseline, lines, target.start.line)
	}
}

func (e *editor) claimedLineSpans(lines []logicalLine) string {
	edits := e.renderedEdits()
	claims := make([]string, 0, min(len(edits), repairListLimit))
	for _, edit := range edits[:min(len(edits), repairListLimit)] {
		start := lineNumberAt(lines, edit.start)
		end := lineNumberAt(lines, max(edit.start, edit.end-1))
		span := strconv.Itoa(start)
		if start != end {
			span = fmt.Sprintf("%d:%d", start, end)
		}
		claims = append(claims, fmt.Sprintf("command %d (%s) line %s", edit.command, edit.operation, span))
	}
	if omitted := len(edits) - len(claims); omitted > 0 {
		claims = append(claims, fmt.Sprintf("... (%d more edits)", omitted))
	}
	return strings.Join(claims, "; ")
}

func writeLineWindow(report *strings.Builder, baseline string, lines []logicalLine, number int) {
	if number < 1 || number > len(lines) {
		return
	}
	start := max(1, number-repairLineWindow)
	end := min(len(lines), number+repairLineWindow)
	for index := start; index <= end; index++ {
		line := lines[index-1]
		limit := 64
		if index == number {
			limit = repairPreviewLimit
		}
		content := lineContent(baseline, line)
		writeHashLine(report, index, content, previewTextLimit(content, limit))
	}
}

func generatedSourceRepair(content string, line, column int) string {
	return generatedSourceRepairForLanguage(content, line, column, "Go")
}

func generatedSourceRepairForLanguage(content string, line, column int, language string) string {
	lines := renderedLines(content)
	if line < 1 || line > len(lines) {
		return ""
	}

	var report strings.Builder
	fmt.Fprintf(&report, "generated %s near %d:%d\n", language, line, column)
	start := max(1, line-repairLineWindow)
	end := min(len(lines), line+repairLineWindow)
	for index := start; index <= end; index++ {
		current := lines[index-1]
		limit := 64
		marker := " "
		if index == line {
			limit = repairPreviewLimit
			marker = ">"
		}
		text := lineContent(content, current)
		fmt.Fprintf(&report, "%s %d | %s\n", marker, index, previewTextLimit(text, limit))
	}
	return report.String()
}

func multilineValueRepair(command int, value string, line int) string {
	lines := physicalValueLines(value)
	if command < 1 || line < 1 || line > len(lines) {
		return ""
	}

	var report strings.Builder
	fmt.Fprintf(&report, "command %d multiline value near row %d\n", command, line)
	start := max(1, line-repairLineWindow)
	end := min(len(lines), line+repairLineWindow)
	for index := start; index <= end; index++ {
		marker := " "
		if index == line {
			marker = ">"
		}
		text := lineContent(value, lines[index-1])
		fmt.Fprintf(&report, "%s value row %d | %s\n", marker, index, previewTextLimit(text, repairPreviewLimit))
	}
	return report.String()
}

func lineNumberAt(lines []logicalLine, offset int) int {
	for index, line := range lines {
		if offset < line.End {
			return index + 1
		}
	}
	return len(lines)
}

func joinLineNumbers(numbers []int, total int) string {
	rendered := make([]string, 0, len(numbers)+1)
	for _, number := range numbers {
		rendered = append(rendered, strconv.Itoa(number))
	}
	if omitted := total - len(numbers); omitted > 0 {
		rendered = append(rendered, fmt.Sprintf("... (%d more occurrences)", omitted))
	}
	return strings.Join(rendered, ", ")
}
