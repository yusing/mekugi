package mekugi

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
)

type indentationCorrectionKind uint8

const (
	indentationCorrectionExact indentationCorrectionKind = iota + 1
	indentationCorrectionPythonWrapper
	indentationCorrectionBracedWrapper
)

type indentationPolicyKind uint8

const (
	indentationPolicyGo indentationPolicyKind = iota + 1
	indentationPolicyAuto
	indentationPolicyReject
)

type indentationWrapperLanguage uint8

const (
	indentationLanguagePython indentationWrapperLanguage = iota + 1
	indentationLanguageJavaScript
	indentationLanguageTypeScript
)

type languageSyntaxFailure struct {
	line    int
	column  int
	kind    string
	missing bool
}

type indentationCandidate struct {
	kind       indentationCorrectionKind
	correction *indentationCorrectionError
	wrapper    indentationWrapperCandidate
}

type indentationWrapperCandidate struct {
	childLine     int
	wrapperIndent string
	preserved     string
}

type preparedWrapperCorrection struct {
	editIndex   int
	sequence    int
	replacement string
	childStart  int
	childEnd    int
	candidate   indentationWrapperCandidate
}

type indentationWrapperProbe struct {
	replacementStart int
	replacementEnd   int
	childStart       int
	childEnd         int
	candidate        indentationWrapperCandidate
}

// indentationPolicy returns the indentation policy for a file path.
func indentationPolicy(path string) indentationPolicyKind {
	if filepath.Ext(path) == ".go" {
		return indentationPolicyGo
	}
	if _, _, ok := languageSyntaxForPath(path); ok {
		return indentationPolicyAuto
	}
	return indentationPolicyReject
}

// detectIndentationCandidate detects if a replacement is an indentation correction candidate.
func detectIndentationCandidate(baseline string, selected targetSpan, replacement string) (indentationCandidate, bool) {
	if !selected.linewise {
		return indentationCandidate{}, false
	}
	if correction := detectIndentationCorrection(baseline, selected, replacement); correction != nil {
		return indentationCandidate{
			kind:       indentationCorrectionExact,
			correction: correction,
		}, true
	}
	return detectIndentationWrapperCandidate(baseline, selected, replacement)
}

// detectIndentationWrapperCandidate detects if a replacement needs wrapper indentation correction.
func detectIndentationWrapperCandidate(baseline string, selected targetSpan, replacement string) (indentationCandidate, bool) {
	selectedLines := logicalLines(baseline[selected.start:selected.end])
	if len(selectedLines) != 1 {
		return indentationCandidate{}, false
	}
	selectedLine := selectedLines[0]
	original := baseline[selected.start+selectedLine.Start : selected.start+selectedLine.ContentEnd]
	originalIndent, preserved := splitIndent(original)
	if preserved == "" || indentationCommentText(preserved) {
		return indentationCandidate{}, false
	}

	replacementLines := logicalLines(replacement)
	if len(replacementLines) != 2 && len(replacementLines) != 3 {
		return indentationCandidate{}, false
	}
	header := replacementLines[0]
	child := replacementLines[1]
	headerContent := replacement[header.Start:header.ContentEnd]
	childContent := replacement[child.Start:child.ContentEnd]
	headerIndent, headerText := splitIndent(headerContent)
	childIndent, childText := splitIndent(childContent)
	if headerIndent != originalIndent ||
		childText != preserved ||
		strings.Count(childContent, preserved) != 1 {
		return indentationCandidate{}, false
	}

	kind := indentationCorrectionPythonWrapper
	if len(replacementLines) == 2 {
		if !pythonIfHeader(headerText) {
			return indentationCandidate{}, false
		}
	} else {
		closeLine := replacementLines[2]
		closeContent := replacement[closeLine.Start:closeLine.ContentEnd]
		closeIndent, closeText := splitIndent(closeContent)
		if closeIndent != originalIndent || closeText != "}" || !bracedIfHeader(headerText) {
			return indentationCandidate{}, false
		}
		kind = indentationCorrectionBracedWrapper
	}
	return indentationCandidate{
		kind: kind,
		wrapper: indentationWrapperCandidate{
			childLine:     1,
			wrapperIndent: originalIndent,
			preserved:     preserved,
		},
		correction: &indentationCorrectionError{
			proposedLine:     childContent,
			proposedIndent:   childIndent,
			correctionIndent: originalIndent,
			correctedText:    replacement,
		},
	}, true
}

func indentationCommentText(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "#") || strings.HasPrefix(value, "//") ||
		strings.HasPrefix(value, "/*") || strings.HasPrefix(value, "*")
}

func pythonIfHeader(header string) bool {
	if !strings.HasPrefix(header, "if") || len(header) < 3 || (header[2] != ' ' && header[2] != '\t') {
		return false
	}
	header = strings.TrimRight(header, " \t")
	if !strings.HasSuffix(header, ":") {
		return false
	}
	condition := strings.TrimSpace(header[2 : len(header)-1])
	return condition != "" && !strings.ContainsAny(condition, "#;")
}

func bracedIfHeader(header string) bool {
	if !strings.HasPrefix(header, "if") || len(header) < 4 || (header[2] != ' ' && header[2] != '\t') {
		return false
	}
	header = strings.TrimRight(header, " \t")
	if !strings.HasSuffix(header, "{") {
		return false
	}
	open := strings.IndexByte(header, '(')
	close := strings.LastIndexByte(header, ')')
	if open < 0 || close <= open || close != len(header)-3 {
		return false
	}
	condition := strings.TrimSpace(header[open+1 : close])
	return condition != "" && !strings.ContainsAny(condition, ";/#")
}

func (e *editor) renderIndentation(ctx context.Context, path string) error {
	language, _, ok := languageSyntaxForPath(path)
	if !ok || !slices.ContainsFunc(e.edits, func(edit baselineEdit) bool {
		return edit.indentation != nil
	}) {
		return nil
	}
	unit := inferIndentationUnit(e.baseline, language)
	var prepared []preparedWrapperCorrection
	for editIndex := range e.edits {
		if err := ctx.Err(); err != nil {
			return err
		}
		indentation := e.edits[editIndex].indentation
		if indentation == nil {
			continue
		}
		candidate := indentation.candidate
		switch candidate.kind {
		case indentationCorrectionExact:
			if e.edits[editIndex].replacement != candidate.correction.correctedText {
				e.edits[editIndex].replacement = candidate.correction.correctedText
				e.projected = nil
			}
		case indentationCorrectionPythonWrapper, indentationCorrectionBracedWrapper:
			if unit == "" ||
				(candidate.kind == indentationCorrectionPythonWrapper && language != indentationLanguagePython) ||
				(candidate.kind == indentationCorrectionBracedWrapper &&
					language != indentationLanguageJavaScript && language != indentationLanguageTypeScript) {
				continue
			}
			corrected, childStart, childEnd, changed, ok := prepareWrapperReplacement(
				e.edits[editIndex].replacement,
				candidate.wrapper,
				unit,
			)
			if !ok || !changed {
				continue
			}
			prepared = append(prepared, preparedWrapperCorrection{
				editIndex:   editIndex,
				sequence:    e.edits[editIndex].sequence,
				replacement: corrected,
				childStart:  childStart,
				childEnd:    childEnd,
				candidate:   candidate.wrapper,
			})
		}
	}
	if len(prepared) == 0 {
		return nil
	}

	probeEdits := slices.Clone(e.edits)
	for _, correction := range prepared {
		probeEdits[correction.editIndex].replacement = correction.replacement
	}
	projection := projectBaselineEdits(probeEdits)
	source := e.contentWithProjection(projection)
	probes := make([]indentationWrapperProbe, 0, len(prepared))
	ranges := make(map[int]renderedSpan, len(projection))
	for _, edit := range projection {
		ranges[edit.sequence] = edit.span
	}
	for _, correction := range prepared {
		span, ok := ranges[correction.sequence]
		if !ok {
			return nil
		}
		probes = append(probes, indentationWrapperProbe{
			replacementStart: span.start,
			replacementEnd:   span.end,
			childStart:       span.start + correction.childStart,
			childEnd:         span.start + correction.childEnd,
			candidate:        correction.candidate,
		})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	proven := proveWrapperMemberships(source, probes, language)
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(proven) != len(prepared) || slices.Contains(proven, false) {
		return nil
	}
	for _, correction := range prepared {
		e.edits[correction.editIndex].replacement = correction.replacement
	}
	e.projected = nil
	return nil
}

func prepareWrapperReplacement(replacement string, candidate indentationWrapperCandidate, unit string) (corrected string, childStart, childEnd int, changed, ok bool) {
	lines := logicalLines(replacement)
	if candidate.childLine < 0 || candidate.childLine >= len(lines) {
		return replacement, 0, 0, false, false
	}
	child := lines[candidate.childLine]
	childContent := replacement[child.Start:child.ContentEnd]
	proposedIndent, preserved := splitIndent(childContent)
	if preserved != candidate.preserved || strings.Count(childContent, candidate.preserved) != 1 {
		return replacement, 0, 0, false, false
	}
	desiredIndent := candidate.wrapperIndent + unit
	if proposedIndent == desiredIndent {
		return replacement, 0, 0, false, true
	}
	corrected = replacement[:child.Start] + desiredIndent + replacement[child.Start+len(proposedIndent):]
	correctedLines := logicalLines(corrected)
	correctedChild := correctedLines[candidate.childLine]
	return corrected,
		correctedChild.Start + len(desiredIndent),
		correctedChild.ContentEnd,
		true,
		true
}
