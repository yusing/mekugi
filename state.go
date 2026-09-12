package mekugi

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type renderedCoordinate struct {
	line   int
	column int
}

type renderedDocument struct {
	content string
	lines   []logicalLine
}

type reportedEdit struct {
	file      *fileState
	command   int
	operation string
	advisory  string
	target    targetSpec
	spans     []renderedSpan
}

func (w *workspace) finalStateReport(changes []change) (string, []TargetAlias) {
	var report strings.Builder
	if w.active == nil {
		report.WriteString("no active file\n")
	} else {
		fmt.Fprintf(&report, "in %s\n", escapeReportControls(w.active.path))
	}

	last := w.lastReportedEdit()
	if last == nil {
		report.WriteString("last none\n")
	} else {
		last.writeSummary(&report)
	}
	w.writeFileSummary(&report, changes)
	for _, edit := range w.reportedEdits {
		if edit.advisory != "" {
			fmt.Fprintf(&report, "advisory %d %s %s: %s\n", edit.command, edit.operation, escapeReportControls(edit.file.path), edit.advisory)
		}
	}
	activeReferences := false
	if len(w.reportedEdits) != 0 {
		activeReferences = w.writeFinalReferences(&report)
	}
	if w.active != nil && !activeReferences {
		w.writeFallbackPreview(&report)
	}
	return report.String(), w.targetAliases()
}

func (w *workspace) lastReportedEdit() *reportedEdit {
	if len(w.reportedEdits) == 0 {
		return nil
	}
	return w.reportedEdits[len(w.reportedEdits)-1]
}

func (e *editor) reportedEdit(origin editOrigin, command instruction) *reportedEdit {
	var spans []renderedSpan
	for _, edit := range e.edits {
		if edit.command == origin.command {
			spans = append(spans, renderedSpan{start: edit.targetStart, end: edit.targetEnd})
		}
	}
	if len(spans) == 0 {
		return nil
	}
	return &reportedEdit{command: origin.command, operation: origin.operation, target: origin.targetSpec, spans: spans, advisory: e.boundaryAdvisory(origin, command)}
}

func (w *workspace) targetAliases() []TargetAlias {
	documents := make(map[*fileState]renderedDocument)
	extents := make(map[*fileState]map[int]renderedSpan)
	var aliases []TargetAlias
	for _, reported := range w.reportedEdits {
		if reported.operation != "type" ||
			(reported.target.kind != targetLine && reported.target.kind != targetRange) {
			continue
		}
		document, ok := documents[reported.file]
		if !ok {
			content := reported.file.editor.content()
			document = renderedDocument{content: content, lines: previewLines(content)}
			documents[reported.file] = document
			extents[reported.file] = reported.file.editor.renderedEditExtents()
		}
		extent, ok := extents[reported.file][reported.command]
		if !ok || extent.start == extent.end {
			continue
		}
		extent = reported.file.editor.finalOffsets.mapExtent(extent)
		startOffset, endOffset := extent.start, extent.end
		startLine := renderedCoordinateAt(document.content, document.lines, startOffset).line
		endLine := renderedCoordinateAt(document.content, document.lines, max(startOffset, endOffset-1)).line
		before := renderRowTarget(reported.target)
		after := renderDocumentRange(document, startLine, endLine)
		aliases = append(aliases, TargetAlias{Path: reported.file.path, Before: before, After: after})
	}
	return aliases
}

func renderRowTarget(target targetSpec) string {
	start := fmt.Sprintf("%d:%s", target.start.line, target.start.hash)
	if target.kind == targetRange {
		return fmt.Sprintf("%s..%d:%s", start, target.end.line, target.end.hash)
	}
	return start
}

func renderDocumentRange(document renderedDocument, startLine, endLine int) string {
	start := document.lines[startLine-1]
	first := fmt.Sprintf("%d:%s", startLine, hashLine(lineContent(document.content, start)))
	if startLine == endLine {
		return first
	}
	end := document.lines[endLine-1]
	return fmt.Sprintf("%s..%d:%s", first, endLine, hashLine(lineContent(document.content, end)))
}

func (e *reportedEdit) writeSummary(report *strings.Builder) {
	document := renderedDocument{content: e.file.editor.baseline, lines: renderedLines(e.file.editor.baseline)}
	fmt.Fprintf(
		report,
		"last %s %s %d ranges ",
		e.operation,
		escapeReportControls(e.file.path),
		len(e.spans),
	)
	writeSpanLocations(report, document, e.spans)
}

func writeSpanLocations(report *strings.Builder, document renderedDocument, spans []renderedSpan) {
	const locationLimit = 3
	for index, span := range spans[:min(len(spans), locationLimit)] {
		if index != 0 {
			report.WriteString(", ")
		}
		start := renderedCoordinateAt(document.content, document.lines, span.start)
		end := renderedCoordinateAt(document.content, document.lines, span.end)
		fmt.Fprintf(report, "%d:%d-%d:%d", start.line, start.column, end.line, end.column)
	}
	if omitted := len(spans) - locationLimit; omitted > 0 {
		fmt.Fprintf(report, " +%d more", omitted)
	}
	report.WriteByte('\n')
}

func (w *workspace) writeFinalReferences(report *strings.Builder) bool {
	documents := make(map[*fileState]renderedDocument)
	extents := make(map[*fileState]map[int]renderedSpan)
	activeReferences := false
	for _, reported := range w.reportedEdits {
		if reported.file == w.active {
			activeReferences = true
		}
		document, ok := documents[reported.file]
		if !ok {
			content := reported.file.editor.content()
			document = renderedDocument{content: content, lines: previewLines(content)}
			documents[reported.file] = document
			extents[reported.file] = reported.file.editor.renderedEditExtents()
		}
		extent, ok := extents[reported.file][reported.command]
		if !ok {
			panic("reported edit has no effective editor splice")
		}
		extent = reported.file.editor.finalOffsets.mapExtent(extent)
		startOffset, endOffset := extent.start, extent.end
		startLine := renderedCoordinateAt(document.content, document.lines, startOffset).line
		endLine := renderedCoordinateAt(document.content, document.lines, endOffset).line
		firstLine, lastLine := min(startLine, endLine), max(startLine, endLine)

		fmt.Fprintf(
			report,
			"refs %d %s %s\n",
			reported.command,
			reported.operation,
			escapeReportControls(reported.file.path),
		)
		indexes := []int{firstLine - 2, firstLine - 1, lastLine - 1, lastLine}
		previous := -1
		for _, index := range indexes {
			if index < 0 || index >= len(document.lines) || index == previous {
				continue
			}
			writePreviewLine(report, document, index)
			previous = index
		}
	}
	return activeReferences
}

func (e *editor) renderedEditExtents() map[int]renderedSpan {
	result := make(map[int]renderedSpan)
	for _, edit := range e.renderedEdits() {
		if extent, ok := result[edit.command]; ok {
			extent.start = min(extent.start, edit.span.start)
			extent.end = max(extent.end, edit.span.end)
			result[edit.command] = extent
		} else {
			result[edit.command] = edit.span
		}
	}
	return result
}

func (w *workspace) writeFallbackPreview(report *strings.Builder) {
	content := w.active.editor.content()
	document := renderedDocument{content: content, lines: previewLines(content)}
	for index := range min(3, len(document.lines)) {
		writePreviewLine(report, document, index)
	}
}

func previewLines(text string) []logicalLine {
	if text == "" {
		return []logicalLine{{}}
	}
	return logicalLines(text)
}

func writePreviewLine(report *strings.Builder, document renderedDocument, index int) {
	line := document.lines[index]
	content := lineContent(document.content, line)
	writeHashLine(report, index+1, content, previewText(content))
}

func (w *workspace) writeFileSummary(report *strings.Builder, changes []change) {
	added, updated, moved, deleted := 0, 0, 0, 0
	for _, change := range changes {
		switch change.kind {
		case changeAdd:
			added++
		case changeDelete:
			deleted++
		case changeUpdate:
			if change.originalPath != change.path {
				moved++
			}
			if change.original != change.content {
				updated++
			}
		}
	}
	fmt.Fprintf(report, "files add=%d update=%d move=%d delete=%d\n", added, updated, moved, deleted)
}

func renderedLines(text string) []logicalLine {
	lines := logicalLines(text)
	if text == "" || endsWithLineTerminator(text) {
		lines = append(lines, logicalLine{Start: len(text), ContentEnd: len(text), End: len(text)})
	}
	return lines
}

func renderedCoordinateAt(text string, lines []logicalLine, offset int) renderedCoordinate {
	offset = min(max(offset, 0), len(text))
	for index, line := range lines {
		if offset <= line.ContentEnd {
			return renderedCoordinate{line: index + 1, column: utf8.RuneCountInString(text[line.Start:offset]) + 1}
		}
		if offset < line.End {
			return renderedCoordinate{line: index + 1, column: utf8.RuneCountInString(text[line.Start:line.ContentEnd]) + 1}
		}
	}
	last := lines[len(lines)-1]
	return renderedCoordinate{line: len(lines), column: utf8.RuneCountInString(text[last.Start:last.ContentEnd]) + 1}
}

func previewText(text string) string {
	return previewTextLimit(text, 64)
}

func previewTextLimit(text string, limit int) string {
	var preview strings.Builder
	count := 0
	leading := true
	for _, character := range text {
		if count == limit {
			break
		}
		count++
		if leading {
			switch character {
			case ' ':
				preview.WriteString(`\x20`)
				continue
			case '\t':
				preview.WriteString(`\t`)
				continue
			default:
				leading = false
			}
		}
		if unicode.IsControl(character) && character != '\t' {
			quoted := strconv.QuoteRune(character)
			preview.WriteString(quoted[1 : len(quoted)-1])
			continue
		}
		preview.WriteRune(character)
	}
	return preview.String()
}

func escapeReportControls(text string) string {
	var escaped strings.Builder
	for _, character := range text {
		if unicode.IsControl(character) {
			quoted := strconv.QuoteRune(character)
			escaped.WriteString(quoted[1 : len(quoted)-1])
			continue
		}
		escaped.WriteRune(character)
	}
	return escaped.String()
}
