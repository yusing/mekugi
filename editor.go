package mekugi

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/verifiedrow"
)

type targetSpan struct {
	start    int
	end      int
	linewise bool
}

type editOrigin struct {
	command        int
	line           int
	operation      string
	target         targetVariant
	targetSpec     targetSpec
	multilineValue bool
}

type baselineEdit struct {
	editOrigin

	start       int
	end         int
	targetStart int
	targetEnd   int
	replacement string
	sequence    int
	indentation *indentationEdit
}

type editor struct {
	baseline     string
	edits        []baselineEdit
	projected    []renderedEdit
	lastOrigin   editOrigin
	finalContent *string
	finalOffsets *formattedOffsetMap
}

type indentationEdit struct {
	candidate indentationCandidate
	command   instruction
	path      string
}

type logicalLine = verifiedrow.Line

// resolveTarget resolves a target specification to baseline byte offsets.
func (e *editor) resolveTarget(target targetSpec) ([]targetSpan, error) {
	switch target.kind {
	case targetLine:
		line, err := e.resolveRow(target.start)
		if err != nil {
			return nil, err
		}
		return []targetSpan{{start: line.Start, end: line.End, linewise: true}}, nil
	case targetRange:
		start, err := e.resolveRow(target.start)
		if err != nil {
			return nil, err
		}
		end, err := e.resolveRow(target.end)
		if err != nil {
			return nil, err
		}
		if start.Start > end.Start {
			return nil, withReason(reasonTargetOrder, fmt.Errorf(
				"resolved row range start %d exceeds end %d",
				lineNumberAt(logicalLines(e.baseline), start.Start),
				lineNumberAt(logicalLines(e.baseline), end.Start),
			))
		}
		return []targetSpan{{start: start.Start, end: end.End, linewise: true}}, nil
	case targetText, targetLiteral:
		search := e.baseline
		baseOffset := 0
		if target.kind == targetText {
			anchor, err := e.resolveRow(target.start)
			if err != nil {
				globalOffsets := nonOverlappingLiteralOffsets(e.baseline, target.literal, target.count)
				if len(globalOffsets) != target.count {
					return nil, err
				}
				afterLast := globalOffsets[len(globalOffsets)-1] + len(target.literal)
				if len(nonOverlappingLiteralOffsets(e.baseline[afterLast:], target.literal, 1)) != 0 {
					return nil, err
				}
				spans := make([]targetSpan, len(globalOffsets))
				for index, offset := range globalOffsets {
					spans[index] = targetSpan{start: offset, end: offset + len(target.literal)}
				}
				return spans, nil
			}
			search = e.baseline[anchor.Start:]
			baseOffset = anchor.Start
		}
		offsets := nonOverlappingLiteralOffsets(search, target.literal, target.count)
		if len(offsets) != target.count {
			if target.kind == targetText {
				return nil, withReason(reasonOccurrenceMissing, fmt.Errorf(
					"found %d of %d requested matches of %q at or after line %d",
					len(offsets),
					target.count,
					target.literal,
					target.start.line,
				))
			}
			return nil, withReason(reasonOccurrenceMissing, fmt.Errorf(
				"found %d of %d requested matches of %q in immutable baseline",
				len(offsets), target.count, target.literal,
			))
		}
		spans := make([]targetSpan, len(offsets))
		for index, offset := range offsets {
			start := baseOffset + offset
			spans[index] = targetSpan{start: start, end: start + len(target.literal)}
		}
		return spans, nil
	case targetEOF:
		return []targetSpan{{start: len(e.baseline), end: len(e.baseline)}}, nil
	default:
		return nil, withReason(reasonInitialization, fmt.Errorf("mutation requires an explicit target"))
	}
}

// resolveRow resolves a row reference to a baseline logical line.
func (e *editor) resolveRow(reference rowReference) (logicalLine, error) {
	resolved, baselineErr := resolveRow(e.baseline, reference)
	if baselineErr == nil || len(e.edits) == 0 {
		return resolved, baselineErr
	}

	pending := e.content()
	pendingLines := logicalLines(pending)
	if reference.line < 1 || reference.line > len(pendingLines) {
		return logicalLine{}, baselineErr
	}
	pendingLine := pendingLines[reference.line-1]
	if hashLine(lineContent(pending, pendingLine)) != reference.hash {
		return logicalLine{}, baselineErr
	}

	var match logicalLine
	matches := 0
	for _, baselineLine := range logicalLines(e.baseline) {
		if hashLine(lineContent(e.baseline, baselineLine)) != reference.hash {
			continue
		}
		start, end, ok := e.renderedBaselineLine(baselineLine)
		if !ok || start != pendingLine.Start || end != pendingLine.End {
			continue
		}
		match = baselineLine
		matches++
	}
	if matches == 1 {
		return match, nil
	}
	return logicalLine{}, baselineErr
}

// renderedBaselineLine computes the rendered offsets of a baseline line after edits.
func (e *editor) renderedBaselineLine(line logicalLine) (int, int, bool) {
	for _, edit := range e.renderedEdits() {
		if edit.start == edit.end {
			if edit.start > line.Start && edit.start < line.End {
				return 0, 0, false
			}
			continue
		}
		if edit.start < line.End && edit.end > line.Start {
			return 0, 0, false
		}
	}
	start := e.renderedBaselineBoundary(line.Start, true)
	end := e.renderedBaselineBoundary(line.End, false)
	return start, end, true
}

// renderedBaselineBoundary maps a baseline offset to its rendered position after edits.
func (e *editor) renderedBaselineBoundary(offset int, includeInsertions bool) int {
	rendered := offset
	for _, edit := range e.renderedEdits() {
		if edit.start == edit.end {
			if edit.start < offset || (includeInsertions && edit.start == offset) {
				rendered += len(edit.replacement)
			}
			continue
		}
		if edit.end <= offset {
			rendered += len(edit.replacement) - (edit.end - edit.start)
		}
	}
	return rendered
}

// resolveRow resolves a row reference in an immutable baseline.
func resolveRow(baseline string, reference rowReference) (logicalLine, error) {
	lines := logicalLines(baseline)
	if reference.line >= 1 && reference.line <= len(lines) {
		line := lines[reference.line-1]
		if hashLine(lineContent(baseline, line)) == reference.hash {
			return line, nil
		}
	}

	var match logicalLine
	matches := 0
	for _, line := range lines {
		if hashLine(lineContent(baseline, line)) != reference.hash {
			continue
		}
		match = line
		matches++
	}
	if matches == 1 {
		return match, nil
	}
	if reference.line < 1 || reference.line > len(lines) {
		if matches == 0 {
			return logicalLine{}, withReason(reasonRowMissing, fmt.Errorf(
				"row %d is outside immutable baseline with %d lines and hash %s is absent",
				reference.line,
				len(lines),
				reference.hash,
			))
		}
		return logicalLine{}, withReason(reasonRowStale, fmt.Errorf(
			"row %d is outside immutable baseline with %d lines and hash %s is ambiguous across %d rows",
			reference.line,
			len(lines),
			reference.hash,
			matches,
		))
	}
	actual := hashLine(lineContent(baseline, lines[reference.line-1]))
	if matches == 0 {
		return logicalLine{}, withReason(reasonRowStale, fmt.Errorf(
			"row %d is stale: expected hash %s, actual %s, expected hash is absent",
			reference.line,
			reference.hash,
			actual,
		))
	}
	return logicalLine{}, withReason(reasonRowStale, fmt.Errorf(
		"row %d is stale: expected hash %s, actual %s, expected hash is ambiguous across %d rows",
		reference.line,
		reference.hash,
		actual,
		matches,
	))
}

// applyMutation applies a type or add mutation to the baseline.
func (e *editor) applyMutation(operation string, target targetSpec, value string, origin editOrigin, command instruction, path string) error {
	spans, err := e.resolveTarget(target)
	if err != nil {
		return err
	}
	edits := make([]baselineEdit, len(spans))
	var candidate indentationCandidate
	var hasCandidate bool
	if operation == "type" && target.kind == targetLine && len(spans) == 1 {
		span := spans[0]
		replacement := value
		if replacement != "" && span.linewise && lineTerminatorSuffix(replacement) == "" {
			replacement += lineTerminatorSuffix(e.baseline[span.start:span.end])
		}
		candidate, hasCandidate = detectIndentationCandidate(e.baseline, span, replacement)
	}
	for index, span := range spans {
		replacement := value
		start, end := span.start, span.end
		switch operation {
		case "type":
			if replacement != "" && span.linewise && lineTerminatorSuffix(replacement) == "" {
				replacement += lineTerminatorSuffix(e.baseline[span.start:span.end])
			}
		case "add":
			end = start
		default:
			panic("parsed instruction has no mutation executor: " + operation)
		}
		edits[index] = baselineEdit{
			start:       start,
			end:         end,
			targetStart: span.start,
			targetEnd:   span.end,
			replacement: replacement,
			editOrigin:  origin,
		}
	}
	if hasCandidate && len(edits) == 1 {
		edits[0].indentation = &indentationEdit{
			candidate: candidate,
			command:   command,
			path:      path,
		}
	}
	if err := e.recordEdits(edits); err != nil {
		return withReason(reasonEditConflict, err)
	}
	return nil
}

// initialize initializes a new file's content.
func (e *editor) initialize(value string, origin editOrigin) {
	e.baseline = ""
	e.edits = nil
	e.projected = nil
	e.finalContent = nil
	e.finalOffsets = nil
	if value == "" {
		return
	}
	e.edits = []baselineEdit{{
		start:       0,
		end:         0,
		targetStart: 0,
		targetEnd:   0,
		replacement: value,
		sequence:    1,
		editOrigin:  origin,
	}}
	e.lastOrigin = origin
}

// recordEdits validates and records edits, checking for conflicts.
func (e *editor) recordEdits(candidates []baselineEdit) error {
	pending := make([]baselineEdit, len(e.edits), len(e.edits)+len(candidates))
	copy(pending, e.edits)
	for _, candidate := range candidates {
		if candidate.start == candidate.end && candidate.replacement == "" {
			continue
		}
		if candidate.start != candidate.end && candidate.replacement == e.baseline[candidate.start:candidate.end] {
			continue
		}
		for _, existing := range pending {
			description, conflict := describeEditConflict(e.baseline, existing, candidate)
			if !conflict {
				continue
			}
			return fmt.Errorf(
				"conflicts with edit from command %d (source line %d, operation %q): %s",
				existing.command,
				existing.line,
				existing.operation,
				description,
			)
		}
		candidate.sequence = len(pending) + 1
		pending = append(pending, candidate)
	}
	if len(pending) != len(e.edits) {
		e.edits = pending
		e.projected = nil
		e.lastOrigin = pending[len(pending)-1].editOrigin
	}
	return nil
}

// describeEditConflict describes a conflict between two baseline edits.
func describeEditConflict(baseline string, first, second baselineEdit) (string, bool) {
	firstInsertion := first.start == first.end
	secondInsertion := second.start == second.end
	switch {
	case firstInsertion && secondInsertion:
		return "", false
	case firstInsertion:
		if first.start <= second.start || first.start >= second.end {
			return "", false
		}
		return fmt.Sprintf("baseline line %d is both replaced and inserted into", baselineLine(baseline, first.start)), true
	case secondInsertion:
		if second.start <= first.start || second.start >= first.end {
			return "", false
		}
		return fmt.Sprintf("baseline line %d is both replaced and inserted into", baselineLine(baseline, second.start)), true
	default:
		start := max(first.start, second.start)
		end := min(first.end, second.end)
		if start >= end {
			return "", false
		}
		startLine := baselineLine(baseline, start)
		endLine := baselineLine(baseline, end-1)
		if startLine == endLine {
			return fmt.Sprintf("baseline line %d is modified by both edits", startLine), true
		}
		return fmt.Sprintf("baseline lines %d:%d are modified by both edits", startLine, endLine), true
	}
}

// baselineLine returns the 1-based line number for a baseline offset.
func baselineLine(text string, offset int) int {
	lines := logicalLines(text)
	for index, line := range lines {
		if offset < line.End {
			return index + 1
		}
	}
	if len(lines) == 0 {
		return 1
	}
	return len(lines)
}

// firstEdit returns the first edit if one exists.
func (e *editor) firstEdit() (baselineEdit, bool) {
	if len(e.edits) == 0 {
		return baselineEdit{}, false
	}
	return e.edits[0], true
}

// orderedBaselineEdits sorts baseline edits by offset and sequence.
func orderedBaselineEdits(source []baselineEdit) []baselineEdit {
	edits := slices.Clone(source)
	slices.SortFunc(edits, func(first, second baselineEdit) int {
		if order := cmp.Compare(first.start, second.start); order != 0 {
			return order
		}
		firstInsertion := first.start == first.end
		secondInsertion := second.start == second.end
		if firstInsertion != secondInsertion {
			if firstInsertion {
				return -1
			}
			return 1
		}
		return cmp.Compare(first.sequence, second.sequence)
	})
	return edits
}

// contentFits checks byte size without concatenating replacement strings.
// Recorded destructive spans are disjoint, so their total cannot exceed the
// baseline. Subtract first to avoid overflow and allow same-command shrinkage.
func (e *editor) contentFits(limit int) bool {
	retained := len(e.baseline)
	for _, edit := range e.edits {
		retained -= edit.end - edit.start
	}
	if retained > limit {
		return false
	}
	remaining := limit - retained
	for _, edit := range e.edits {
		if len(edit.replacement) > remaining {
			return false
		}
		remaining -= len(edit.replacement)
	}
	return true
}

// content returns the editor's current rendered content.
func (e *editor) content() string {
	if e.finalContent != nil {
		return *e.finalContent
	}
	return e.contentWithProjection(e.renderedEdits())
}

// contentWithEdits renders content with a specific set of edits.
func (e *editor) contentWithEdits(source []baselineEdit) string {
	return e.contentWithProjection(projectBaselineEdits(source))
}

// nonOverlappingLiteralOffsets finds non-overlapping occurrences of literal in text.
func nonOverlappingLiteralOffsets(text, literal string, limit int) []int {
	return findLiteralOffsets(text, literal, len(literal), limit)
}

// findLiteralOffsets finds literal occurrences with a custom advance step.
func findLiteralOffsets(text, literal string, advance, limit int) []int {
	var offsets []int
	for searchFrom := 0; searchFrom <= len(text)-len(literal); {
		relative := strings.Index(text[searchFrom:], literal)
		if relative < 0 {
			break
		}
		match := searchFrom + relative
		offsets = append(offsets, match)
		searchFrom = match + advance
		if limit > 0 && len(offsets) == limit {
			break
		}
	}
	return offsets
}

// logicalLines splits text into logical lines using shared verified-row semantics.
func logicalLines(text string) []logicalLine {
	return verifiedrow.Lines(text)
}

// lineTerminatorSuffix returns the line terminator suffix of text.
func lineTerminatorSuffix(text string) string {
	switch {
	case strings.HasSuffix(text, "\r\n"):
		return "\r\n"
	case strings.HasSuffix(text, "\n"):
		return "\n"
	case strings.HasSuffix(text, "\r"):
		return "\r"
	default:
		return ""
	}
}

// endsWithLineTerminator reports whether text ends with a line terminator.
func endsWithLineTerminator(text string) bool {
	return lineTerminatorSuffix(text) != ""
}
