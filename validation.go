package mekugi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/hpatchsyntax"
	"github.com/yusing/mekugi/internal/sourcekind"
)

type indentationCorrectionError struct {
	proposedLine     string
	proposedIndent   string
	correctionIndent string
	correctedText    string
}

func (e *indentationCorrectionError) Error() string {
	return "indentation-only change to preserved text"
}

func (e *indentationCorrectionError) diagnostic() string {
	var output strings.Builder
	fmt.Fprintf(&output, "proposed text: %s\n", strconv.Quote(e.proposedLine))
	fmt.Fprintf(
		&output,
		"indentation: proposed=%s correction=%s\n",
		strconv.Quote(e.proposedIndent),
		strconv.Quote(e.correctionIndent),
	)
	return output.String()
}

// detectIndentationCorrection checks if a replacement is an indentation-only change to preserved text.
func detectIndentationCorrection(baseline string, selected targetSpan, replacement string) *indentationCorrectionError {
	if !selected.linewise {
		return nil
	}
	selectedLines := logicalLines(baseline[selected.start:selected.end])
	if len(selectedLines) != 1 {
		return nil
	}
	selectedLine := selectedLines[0]
	original := baseline[selected.start+selectedLine.Start : selected.start+selectedLine.ContentEnd]
	originalIndent, preserved := splitIndent(original)
	if preserved == "" {
		return nil
	}

	replacementLines := logicalLines(replacement)
	if len(replacementLines) != 1 {
		return nil
	}
	line := replacementLines[0]
	content := replacement[line.Start:line.ContentEnd]
	proposedIndent, remainder := splitIndent(content)
	if remainder != preserved || proposedIndent == originalIndent {
		return nil
	}

	corrected := replacement[:line.Start] + originalIndent + replacement[line.Start+len(proposedIndent):]
	return &indentationCorrectionError{
		proposedLine:     content,
		proposedIndent:   proposedIndent,
		correctionIndent: originalIndent,
		correctedText:    corrected,
	}
}

// splitIndent splits a line into its leading whitespace and remaining content.
func splitIndent(line string) (string, string) {
	end := 0
	for end < len(line) && (line[end] == ' ' || line[end] == '\t') {
		end++
	}
	return line[:end], line[end:]
}

// hostRejectionsOf extracts structured host rejections from an error.
func hostRejectionsOf(err error) []HostRejection {
	commands := commandsOf(err)
	rejections := make([]HostRejection, 0, len(commands))
	for _, command := range commands {
		locations := command.Locations
		if len(locations) == 0 {
			locations = []commandErrorLocation{{
				GeneratedLine:   command.GeneratedLine,
				GeneratedColumn: command.GeneratedColumn,
				ValueLine:       command.ValueLine,
			}}
		}
		for _, location := range locations {
			rejections = append(rejections, HostRejection{
				Command:         command.Command,
				SourceLine:      command.Line,
				Operation:       command.Operation,
				Target:          hostTargetName(command.Target),
				Reason:          hostReasonName(command.Reason),
				Path:            command.Path,
				GeneratedLine:   location.GeneratedLine,
				GeneratedColumn: location.GeneratedColumn,
				ValueLine:       location.ValueLine,
			})
		}
	}
	return rejections
}

// hostFailuresOf extracts actionable host failures from an error.
func hostFailuresOf(err error, failureStage string) []HostFailure {
	commands := commandsOf(err)
	if len(commands) == 0 {
		if failureStage == "" {
			return nil
		}
		return []HostFailure{{Reason: failureStage + "-failure", Scope: "new-transaction"}}
	}
	languageCommands := make(map[int]struct{})
	for _, command := range commands {
		if command.Reason == reasonLanguageSyntax {
			languageCommands[command.Command] = struct{}{}
		}
	}
	failures := make([]HostFailure, 0, len(commands))
	for _, command := range commands {
		scope := "field-local"
		switch command.Reason {
		case reasonEditConflict:
			scope = "multi-command"
			if command.CorrectionScope != "" {
				scope = command.CorrectionScope
			}
		case reasonActiveFile, reasonInitialization, reasonFilePath:
			scope = "new-script"
		case reasonLanguageSyntax:
			if len(languageCommands) > 1 {
				scope = "multi-command"
			}
		}
		failures = append(failures, HostFailure{
			Command:    command.Command,
			Path:       command.Path,
			Reason:     hostReasonName(command.Reason),
			Scope:      scope,
			Suggestion: strings.TrimSpace(command.Repair),
		})
	}
	return failures
}

// hostTargetName converts a target variant to its host-facing name.
func hostTargetName(target targetVariant) string {
	index := int(target) - 1
	if index < 0 || index >= len(targetVariantNames) {
		return ""
	}
	return targetVariantNames[index]
}

// hostReasonName converts a failure reason to its host-facing name.
func hostReasonName(reason failureReason) string {
	if int(reason) < 0 || int(reason) >= len(failureReasonNames) {
		return failureReasonNames[reasonOther]
	}
	return failureReasonNames[reason]
}

func (w *workspace) renderFinal(ctx context.Context) error {
	var failures []*commandError
	for _, file := range w.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file.deleted {
			continue
		}
		fileFailures, err := file.renderContent(ctx)
		if err != nil {
			return err
		}
		failures = append(failures, fileFailures...)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return groupValidationFailures(failures)
}

// renderContent renders the final file content with formatting and validation.
func (file *fileState) renderContent(ctx context.Context) ([]*commandError, error) {
	if err := file.editor.renderIndentation(ctx, file.path); err != nil {
		return nil, err
	}
	file.editor.finalContent = nil
	file.editor.finalOffsets = nil
	rendered := file.editor.contentWithProjection(file.editor.renderedEdits())
	final := rendered
	var offsets *formattedOffsetMap
	var failures []*commandError
	if filepath.Ext(file.path) == ".go" &&
		(file.created || file.original != rendered || filepath.Ext(file.originalPath) != ".go") {
		formatted, err := format.Source([]byte(rendered))
		if err != nil {
			for _, failure := range discoverGoSyntaxFailures(ctx, rendered, err) {
				location := file.editor.syntaxFailureLocation(rendered, failure.line, failure.column)
				if location.origin.command == 0 {
					location.origin = file.validationOrigin()
				}
				repair := generatedSourceRepair(rendered, failure.line, failure.column)
				repair += multilineValueRepair(location.origin.command, location.replacement, location.valueLine)
				commandFailure := formatCommandError(
					file, location.origin, reasonLanguageSyntax, failure.message,
					repair, failure.line, failure.column, location.valueLine,
				)
				if !failure.counted {
					commandFailure.Occurrences = 0
				}
				failures = append(failures, commandFailure)
			}
			return failures, ctx.Err()
		}
		final = string(formatted)
		if final != rendered {
			offsets, err = newFormattedOffsetMap(rendered, final)
			if err != nil {
				return []*commandError{formatCommandError(
					file,
					file.validationOrigin(),
					reasonOther,
					fmt.Sprintf("map formatted Go source: %v", err),
					"",
					0,
					0,
					0,
				)}, nil
			}
		}
	}

	language, name, supported := languageSyntaxForPath(file.path)
	if supported && (file.created || file.originalPath != file.path || file.original != final) {
		syntaxFailures := collapseLanguageSyntaxCascades(ctx, final, language, findLanguageSyntaxFailures(final, language))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, failure := range syntaxFailures {
			location := file.editor.languageSyntaxFailureLocation(final, failure.line, failure.column, language)
			if location.origin.command == 0 {
				location.origin = file.validationOrigin()
			}
			repair := generatedSourceRepairForLanguage(final, failure.line, failure.column, name)
			repair += multilineValueRepair(location.origin.command, location.replacement, location.valueLine)
			failures = append(failures, formatCommandError(
				file,
				location.origin,
				reasonLanguageSyntax,
				languageSyntaxFailureMessage(name, failure),
				repair,
				failure.line,
				failure.column,
				location.valueLine,
			))
		}
	}
	if len(failures) == 0 {
		file.editor.finalContent = new(final)
		file.editor.finalOffsets = offsets
	}
	return failures, nil
}

// languageSyntaxForPath determines the language and syntax checker for a file path.
func languageSyntaxForPath(path string) (indentationWrapperLanguage, string, bool) {
	format, ok := sourcekind.Classify(path)
	if !ok || !format.SyntaxValidation {
		return 0, "", false
	}
	switch format.Language {
	case "python":
		return indentationLanguagePython, "Python", true
	case "javascript":
		return indentationLanguageJavaScript, "JavaScript", true
	case "typescript":
		return indentationLanguageTypeScript, "TypeScript", true
	default:
		return 0, "", false
	}
}

// languageSyntaxFailureMessage formats a language syntax failure as a message.
func languageSyntaxFailureMessage(name string, failure languageSyntaxFailure) string {
	if failure.missing {
		if failure.kind != "" {
			return fmt.Sprintf("parse %s source: missing %q at %d:%d", name, failure.kind, failure.line, failure.column)
		}
		return fmt.Sprintf("parse %s source: missing syntax at %d:%d", name, failure.line, failure.column)
	}
	if failure.kind != "" {
		return fmt.Sprintf("parse %s source: syntax error %q at %d:%d", name, failure.kind, failure.line, failure.column)
	}
	return fmt.Sprintf("parse %s source: syntax error at %d:%d", name, failure.line, failure.column)
}

func (f *fileState) validationOrigin() editOrigin {
	if f.editor.lastOrigin.command != 0 {
		return f.editor.lastOrigin
	}
	return f.mutationOrigin
}

type goSyntaxFailure struct {
	line    int
	column  int
	message string
	counted bool
}

// goSyntaxFailures extracts Go syntax failures from a scanner or parser error.
func goSyntaxFailures(err error) []goSyntaxFailure {
	if scannerFailures, ok := errors.AsType[scanner.ErrorList](err); ok && len(scannerFailures) != 0 {
		failures := make([]goSyntaxFailure, 0, len(scannerFailures))
		for _, failure := range scannerFailures {
			if failure == nil {
				continue
			}
			failures = append(failures, goSyntaxFailure{
				line:    failure.Pos.Line,
				column:  failure.Pos.Column,
				message: failure.Msg,
				counted: true,
			})
		}
		if len(failures) != 0 {
			return failures
		}
	}
	return []goSyntaxFailure{{message: err.Error(), counted: true}}
}

// parseGoSyntaxFailures parses Go source and returns syntax failures.
func parseGoSyntaxFailures(source string) []goSyntaxFailure {
	_, err := parser.ParseFile(
		token.NewFileSet(),
		"",
		source,
		parser.AllErrors|parser.ParseComments|parser.SkipObjectResolution,
	)
	if err == nil {
		return nil
	}
	return goSyntaxFailures(err)
}

// goSyntaxFailuresForSource parses source and merges failures with a fallback error.
func goSyntaxFailuresForSource(source string, fallback error) []goSyntaxFailure {
	failures := parseGoSyntaxFailures(source)
	if len(failures) == 0 {
		if fallback == nil {
			return nil
		}
		return goSyntaxFailures(fallback)
	}
	if fallback == nil {
		return failures
	}

	type position struct {
		line   int
		column int
	}
	counted := make(map[position]int)
	for _, failure := range goSyntaxFailures(fallback) {
		counted[position{line: failure.line, column: failure.column}]++
	}
	for index := range failures {
		key := position{line: failures[index].line, column: failures[index].column}
		failures[index].counted = counted[key] > 0
		if failures[index].counted {
			counted[key]--
		}
	}
	return failures
}

// discoverGoSyntaxFailures iteratively discovers Go syntax failures by blanking lines.
func discoverGoSyntaxFailures(ctx context.Context, content string, initial error) []goSyntaxFailure {
	candidate := content
	fallback := initial
	seenLines := make(map[int]bool)
	var discovered []goSyntaxFailure
	for {
		if ctx.Err() != nil {
			return discovered
		}
		failures := goSyntaxFailuresForSource(candidate, fallback)
		fallback = nil
		if len(failures) == 0 {
			return discovered
		}
		collapsed := collapseGoSyntaxCascades(ctx, candidate, failures)
		newLines := make(map[int]bool)
		for _, failure := range collapsed {
			if failure.line > 0 && !seenLines[failure.line] {
				newLines[failure.line] = true
			}
		}
		if len(newLines) == 0 {
			if len(discovered) == 0 {
				discovered = append(discovered, collapsed...)
			}
			return discovered
		}
		for _, failure := range collapsed {
			if newLines[failure.line] {
				discovered = append(discovered, failure)
			}
		}
		for line := range newLines {
			seenLines[line] = true
			var ok bool
			candidate, ok = blankGeneratedLine(candidate, line)
			if !ok {
				return discovered
			}
		}
	}
}

// collapseGoSyntaxCascades keeps Go's original occurrence accounting while
// projecting cascades to their earlier repair diagnostic.
func collapseGoSyntaxCascades(ctx context.Context, content string, failures []goSyntaxFailure) []goSyntaxFailure {
	linesOf := func(failures []goSyntaxFailure) []int {
		lines := make([]int, len(failures))
		for i, failure := range failures {
			lines[i] = failure.line
		}
		return lines
	}
	origins := syntaxCascadeOrigins(ctx, content, linesOf(failures), func(source string) []int {
		return linesOf(parseGoSyntaxFailures(source))
	})
	collapsed := make([]goSyntaxFailure, len(origins))
	for i, origin := range origins {
		collapsed[i] = failures[origin]
		collapsed[i].counted = failures[i].counted
	}
	return collapsed
}

// collapseLanguageSyntaxCascades retains the selected Tree-sitter diagnostic's
// complete payload, including its missing-node identity.
func collapseLanguageSyntaxCascades(ctx context.Context, content string, language indentationWrapperLanguage, failures []languageSyntaxFailure) []languageSyntaxFailure {
	linesOf := func(failures []languageSyntaxFailure) []int {
		lines := make([]int, len(failures))
		for i, failure := range failures {
			lines[i] = failure.line
		}
		return lines
	}
	origins := syntaxCascadeOrigins(ctx, content, linesOf(failures), func(source string) []int {
		return linesOf(findLanguageSyntaxFailures(source, language))
	})
	collapsed := make([]languageSyntaxFailure, len(origins))
	for i, origin := range origins {
		collapsed[i] = failures[origin]
	}
	return collapsed
}

type validationFailureGroupKey struct {
	command int
	path    string
}

// groupValidationFailures groups and deduplicates validation failures by command and location.
func groupValidationFailures(failures []*commandError) error {
	if len(failures) == 0 {
		return nil
	}
	slices.SortFunc(failures, func(first, second *commandError) int {
		if order := cmp.Compare(first.Command, second.Command); order != 0 {
			return order
		}
		if order := cmp.Compare(first.Path, second.Path); order != 0 {
			return order
		}
		if order := cmp.Compare(first.ValueLine, second.ValueLine); order != 0 {
			return order
		}
		if order := cmp.Compare(first.GeneratedLine, second.GeneratedLine); order != 0 {
			return order
		}
		return cmp.Compare(first.GeneratedColumn, second.GeneratedColumn)
	})

	indices := make(map[validationFailureGroupKey]int)
	groups := make([]*commandError, 0, len(failures))
	for _, failure := range failures {
		key := validationFailureGroupKey{command: failure.Command, path: failure.Path}
		index, ok := indices[key]
		if !ok {
			index = len(groups)
			indices[key] = index
			group := *failure
			group.Repair = ""
			group.Locations = nil
			groups = append(groups, &group)
		}
		group := groups[index]
		locationIndex := slices.IndexFunc(group.Locations, func(location commandErrorLocation) bool {
			return location.ValueLine == failure.ValueLine
		})
		if locationIndex >= 0 {
			group.Locations[locationIndex].Occurrences += failure.Occurrences
			continue
		}
		group.Locations = append(group.Locations, commandErrorLocation{
			Message:         failure.Message,
			Repair:          failure.Repair,
			GeneratedLine:   failure.GeneratedLine,
			GeneratedColumn: failure.GeneratedColumn,
			ValueLine:       failure.ValueLine,
			Occurrences:     max(failure.Occurrences, 1),
		})
	}

	for _, group := range groups {
		first := group.Locations[0]
		group.GeneratedLine = first.GeneratedLine
		group.GeneratedColumn = first.GeneratedColumn
		group.ValueLine = first.ValueLine
		if len(group.Locations) == 1 {
			group.Message = first.Message
			if first.Occurrences > 1 {
				group.Message = fmt.Sprintf("%s (and %d more errors)", group.Message, first.Occurrences-1)
			}
		} else {
			group.Message = fmt.Sprintf("%d distinct syntax failures", len(group.Locations))
		}
		for _, location := range group.Locations {
			if location.Repair == "" {
				continue
			}
			if group.Repair != "" {
				group.Repair += "\n"
			}
			group.Repair += location.Repair
		}
	}
	if len(groups) == 1 {
		return groups[0]
	}
	return &commandGroupError{commands: groups}
}

const syntaxLocalizationGroupLimit = 32

type syntaxEditGroup struct {
	origin   editOrigin
	edits    []baselineEdit
	distance int

	replacement string
	valueLine   int
}

type syntaxFailureLocation struct {
	origin      editOrigin
	replacement string
	valueLine   int
}

// syntaxFailureLocation localizes a Go syntax failure to the causative edit.
func (e *editor) syntaxFailureLocation(content string, line, column int) syntaxFailureLocation {
	return e.syntaxFailureLocationWith(content, line, column, func(source string) bool {
		_, err := format.Source([]byte(source))
		return err == nil
	})
}

// languageSyntaxFailureLocation localizes a language syntax failure to the causative edit.
func (e *editor) languageSyntaxFailureLocation(content string, line, column int, language indentationWrapperLanguage) syntaxFailureLocation {
	return e.syntaxFailureLocationWith(content, line, column, func(source string) bool {
		return len(findLanguageSyntaxFailures(source, language)) == 0
	})
}

// syntaxFailureLocationWith localizes a syntax failure using a custom validity checker.
func (e *editor) syntaxFailureLocationWith(content string, line, column int, valid func(string) bool) syntaxFailureLocation {
	generatedOffset := generatedByteOffset(content, line, column)
	groups := e.syntaxEditGroups(generatedOffset, len(content))
	if len(groups) == 0 {
		return syntaxFailureLocation{origin: e.lastOrigin}
	}
	if len(groups) == 1 || len(groups) > syntaxLocalizationGroupLimit {
		return syntaxLocationOf(closestSyntaxEditGroup(groups))
	}
	if !valid(e.baseline) {
		return syntaxLocationOf(closestSyntaxEditGroup(groups))
	}

	// Remove far-away groups first. The remaining set is one-minimal: removing
	// any retained command makes the candidate source syntactically valid.
	slices.SortFunc(groups, func(first, second syntaxEditGroup) int {
		if order := cmp.Compare(second.distance, first.distance); order != 0 {
			return order
		}
		return cmp.Compare(first.origin.command, second.origin.command)
	})
	for index := 0; index < len(groups); {
		candidate := slices.Concat(groups[:index], groups[index+1:])
		if !valid(e.contentWithSyntaxGroups(candidate)) {
			groups = candidate
			continue
		}
		index++
	}
	return syntaxLocationOf(closestSyntaxEditGroup(groups))
}

// syntaxLocationOf extracts a syntaxFailureLocation from a syntaxEditGroup.
func syntaxLocationOf(group syntaxEditGroup) syntaxFailureLocation {
	return syntaxFailureLocation{origin: group.origin, replacement: group.replacement, valueLine: group.valueLine}
}

// syntaxEditGroups groups edits by command and computes their distance from a failure offset.
func (e *editor) syntaxEditGroups(generatedOffset, contentLength int) []syntaxEditGroup {
	indices := make(map[int]int)
	var groups []syntaxEditGroup
	for _, edit := range e.edits {
		index, ok := indices[edit.command]
		if !ok {
			index = len(groups)
			indices[edit.command] = index
			groups = append(groups, syntaxEditGroup{origin: edit.editOrigin, distance: contentLength})
		}
		groups[index].edits = append(groups[index].edits, edit)
	}

	for _, edit := range e.renderedEdits() {
		start, end := edit.span.start, edit.span.end
		distance := max(start-generatedOffset, generatedOffset-end, 0)
		index := indices[edit.command]
		if distance <= groups[index].distance {
			groups[index].distance = distance
			if edit.multilineValue {
				groups[index].replacement = edit.replacement
				groups[index].valueLine = replacementValueLine(edit.replacement, generatedOffset-start)
			}
		}
	}
	return groups
}

// replacementValueLine returns the 1-based value line number for an offset within a replacement.
func replacementValueLine(replacement string, offset int) int {
	lines := physicalValueLines(replacement)
	if len(lines) == 0 {
		return 0
	}
	offset = min(max(offset, 0), len(replacement))
	return lineNumberAt(lines, offset)
}

// physicalValueLines splits a value into physical lines for multiline value reporting.
func physicalValueLines(value string) []logicalLine {
	physical := hpatchsyntax.SplitPhysicalLines(value)
	lines := make([]logicalLine, 0, len(physical))
	offset := 0
	for _, line := range physical {
		if line.Text == "" && line.Terminator == "" && offset == len(value) {
			break
		}
		contentEnd := offset + len(line.Text)
		fullEnd := contentEnd + len(line.Terminator)
		lines = append(lines, logicalLine{Start: offset, ContentEnd: contentEnd, End: fullEnd})
		offset = fullEnd
	}
	return lines
}

// closestSyntaxEditGroup returns the edit group closest to a syntax failure.
func closestSyntaxEditGroup(groups []syntaxEditGroup) syntaxEditGroup {
	return slices.MinFunc(groups, func(first, second syntaxEditGroup) int {
		if order := cmp.Compare(first.distance, second.distance); order != 0 {
			return order
		}
		return cmp.Compare(second.origin.command, first.origin.command)
	})
}

// contentWithSyntaxGroups renders content with only the edits from the given groups.
func (e *editor) contentWithSyntaxGroups(groups []syntaxEditGroup) string {
	var edits []baselineEdit
	for _, group := range groups {
		edits = append(edits, group.edits...)
	}
	return e.contentWithEdits(edits)
}

// generatedByteOffset converts a line and column position to a byte offset.
func generatedByteOffset(content string, line, column int) int {
	lines := renderedLines(content)
	if line < 1 || line > len(lines) {
		return len(content)
	}
	current := lines[line-1]
	return min(current.Start+max(column-1, 0), current.ContentEnd)
}

// formatCommandError creates a command error for validation failures.
func formatCommandError(file *fileState, origin editOrigin, reason failureReason, message, repair string, generatedLine, generatedColumn, valueLine int) *commandError {
	category := ""
	if origin.operation != "" {
		category = commandCategory(origin.operation)
	}
	return &commandError{
		Target:          origin.target,
		Reason:          reason,
		Command:         origin.command,
		Line:            origin.line,
		Operation:       origin.operation,
		Path:            file.path,
		Category:        category,
		Message:         message,
		Occurrences:     1,
		Repair:          repair,
		GeneratedLine:   generatedLine,
		GeneratedColumn: generatedColumn,
		ValueLine:       valueLine,
	}
}
