package mekugi

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/hpatchsyntax"
	"github.com/yusing/mekugi/internal/verifiedrow"
)

var positiveDecimalPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

type targetKind uint8

const (
	targetNone targetKind = iota
	targetLine
	targetRange
	targetText
	targetLiteral
	targetEOF
)

type rowReference struct {
	line int
	hash string
}

type targetSpec struct {
	kind    targetKind
	start   rowReference
	end     rowReference
	literal string
	count   int
}

func (t targetSpec) variant() targetVariant {
	switch t.kind {
	case targetLine:
		return targetVariantLine
	case targetRange:
		return targetVariantRange
	case targetText, targetLiteral:
		if t.count > 1 {
			return targetVariantTextMultiple
		}
		return targetVariantTextSingle
	default:
		return targetVariantNone
	}
}

type instruction struct {
	source     string
	line       int
	operation  string
	path       string
	target     targetSpec
	text       string
	valueStart int

	delimiter      string
	lineTerminator string
}

type program struct {
	paths        []string
	instructions []instruction
}

type commandGroupError struct {
	commands []*commandError
}

func (e *commandGroupError) Error() string {
	messages := make([]string, len(e.commands))
	for index, command := range e.commands {
		messages[index] = command.Error()
	}
	return strings.Join(messages, "\n")
}

func (e *commandGroupError) Unwrap() []error {
	failures := make([]error, len(e.commands))
	for index, command := range e.commands {
		failures[index] = command
	}
	return failures
}

func commandsOf(err error) []*commandError {
	if failures, ok := errors.AsType[*commandGroupError](err); ok {
		return failures.commands
	}
	if command, ok := errors.AsType[*commandError](err); ok {
		return []*commandError{command}
	}
	return nil
}

type commandErrorLocation struct {
	Message         string
	Repair          string
	GeneratedLine   int
	GeneratedColumn int
	ValueLine       int
	Occurrences     int
}

type commandError struct {
	Target          targetVariant
	Reason          failureReason
	Command         int
	Line            int
	Operation       string
	Path            string
	Category        string
	Source          string
	Message         string
	GeneratedLine   int
	GeneratedColumn int
	ValueLine       int
	Occurrences     int
	// Repair is multi-line baseline context that a retry needs in order to
	// correct this command. It is excluded from Error, whose result is
	// sanitized onto one line, and is emitted separately.
	Repair          string
	Locations       []commandErrorLocation
	CorrectionScope string
}

func (e *commandError) Error() string {
	prefix := e.Operation
	if prefix == "" {
		prefix = "command"
	}
	var context []string
	if e.Command != 0 {
		context = append(context, fmt.Sprintf("command %d", e.Command))
	}
	if e.Path != "" {
		context = append(context, fmt.Sprintf("path %q", e.Path))
	}
	if int(e.Reason) < len(failureReasonNames) {
		context = append(context, "reason "+failureReasonNames[e.Reason])
	}
	return fmt.Sprintf("%s: %s: %s", prefix, strings.Join(context, ", "), e.Message)
}

func parse(source string) (*program, error) {
	parsed, _, err := parseSource(source, "", 0)
	return parsed, err
}

func parseFileEdits(edits []FileEdit) (*program, error) {
	program := &program{}
	var failures []*commandError
	commandOffset := 0
	for _, edit := range edits {
		if edit.Path == "" {
			_, commandCount, _ := parseSource(edit.Script, edit.Path, commandOffset)
			commandOffset += commandCount
			failures = append(failures, &commandError{
				Reason:    reasonPath,
				Command:   commandOffset - commandCount + 1,
				Line:      1,
				Operation: "file",
				Category:  "path",
				Message:   "file edit path must not be empty",
			})
			continue
		}
		program.paths = append(program.paths, edit.Path)
		parsed, commandCount, err := parseSource(edit.Script, edit.Path, commandOffset)
		commandOffset += commandCount
		if err != nil {
			failures = append(failures, commandsOf(err)...)
			continue
		}
		program.instructions = append(program.instructions, parsed.instructions...)
	}
	if len(failures) != 0 {
		return nil, commandFailures(failures)
	}
	return program, nil
}

func parseSource(source, path string, commandOffset int) (*program, int, error) {
	program := &program{}
	var failures []*commandError
	commandIndex := commandOffset
	lines := hpatchsyntax.SplitPhysicalLines(source)
	for index := 0; index < len(lines); {
		headerIndex := index
		line := lines[headerIndex].Text
		index++
		if strings.TrimSpace(line) == "" {
			continue
		}
		commandIndex++
		sourceLine := headerIndex + 1

		frame, frameErr := hpatchsyntax.FrameCommand(lines, headerIndex, line)
		index = frame.Next
		var command instruction
		var err error
		switch {
		case frameErr != nil:
			// Reuse the ordinary parser only to retain any target variant that was
			// recognized before malformed framing stopped this command.
			command, _ = parseInstruction(sourceLine, line)
			err = scriptError(sourceLine, frameErr.Error())
		case frame.Marker != "":
			header := strings.TrimSuffix(line, " "+frame.Marker)
			command, err = parseInstructionWithValue(sourceLine, header, frame.Body, true)
		default:
			command, err = parseInstruction(sourceLine, line)
		}
		if err != nil {
			message := err.Error()
			if sourceError, ok := errors.AsType[*commandError](err); ok {
				message = sourceError.Message
			}
			operation := ""
			if fields := strings.Fields(line); len(fields) != 0 {
				operation = fields[0]
			}
			failures = append(failures, &commandError{
				Target:    command.target.variant(),
				Reason:    reasonOf(err, reasonSyntax),
				Command:   commandIndex,
				Line:      sourceLine,
				Operation: operation,
				Path:      path,
				Category:  "syntax",
				Source:    line,
				Message:   message,
			})
			continue
		}
		command.source = line
		command.path = path
		command.delimiter = frame.Marker
		command.lineTerminator = lines[headerIndex].Terminator
		program.instructions = append(program.instructions, command)
	}
	if len(failures) != 0 {
		return nil, commandIndex - commandOffset, commandFailures(failures)
	}
	return program, commandIndex - commandOffset, nil
}

func parseInstruction(sourceLine int, line string) (instruction, error) {
	operation, _, ok := strings.Cut(line, " ")
	if !ok {
		return instruction{}, scriptError(sourceLine, "unknown or malformed command")
	}
	switch operation {
	case "type", "add", "append":
		return parseInstructionWithValue(sourceLine, line, "", false)
	default:
		return instruction{}, scriptError(sourceLine, "unknown or malformed command")
	}
}

func parseInstructionWithValue(sourceLine int, line, heredocValue string, heredoc bool) (instruction, error) {
	operation, operands, ok := strings.Cut(line, " ")
	if !ok && heredoc {
		operation, operands, ok = line, "", true
	}
	if !ok || (operation != "type" && operation != "add" && operation != "append") {
		return instruction{}, scriptError(sourceLine, "heredoc is valid only for type, add, or append")
	}
	command := instruction{line: sourceLine, operation: operation}
	if operation == "append" {
		if heredoc {
			if strings.TrimSpace(operands) != "" {
				return command, scriptError(sourceLine, "trailing text before heredoc value")
			}
			command.text = heredocValue
			return command, nil
		}
		trailing := strings.TrimLeft(operands, " \t")
		if trailing == "" {
			return command, scriptError(sourceLine, "append requires a value")
		}
		command.valueStart = len(line) - len(trailing)
		value, trailing, err := hpatchsyntax.DecodeQuoted(trailing)
		if err != nil {
			return command, scriptError(sourceLine, "invalid quoted string for append: "+err.Error())
		}
		if !onlyOperandWhitespace(trailing) {
			return command, scriptError(sourceLine, "trailing text after append value")
		}
		command.text = value
		return command, nil
	}
	target, trailing, err := parseTarget(sourceLine, operands, !heredoc)
	command.target = target
	if err != nil {
		return command, err
	}
	if operation == "add" && target.kind == targetRange {
		return command, scriptError(sourceLine, "add requires a line or text destination")
	}
	value := heredocValue
	if heredoc {
		if strings.TrimSpace(trailing) != "" {
			return command, scriptError(sourceLine, "trailing text before heredoc value")
		}
	} else {
		trailing = strings.TrimLeft(trailing, " \t")
		if trailing == "" {
			return command, scriptError(sourceLine, operation+" requires a value")
		}
		command.valueStart = len(line) - len(trailing)
		value, trailing, err = hpatchsyntax.DecodeQuoted(trailing)
		if err != nil {
			return command, scriptError(sourceLine, "invalid quoted string for "+operation+": "+err.Error())
		}
		if !onlyOperandWhitespace(trailing) {
			return command, scriptError(sourceLine, "trailing text after "+operation+" value")
		}
	}
	command.text = value
	return command, nil
}

// parseTarget parses a target prefix. When finalValueFollows is false, a quoted
// operand after ROW is the target literal. When true, a lone quoted operand is
// the mutation value and therefore leaves a line target.
func parseTarget(sourceLine int, operands string, finalValueFollows bool) (targetSpec, string, error) {
	trimmedOperands := strings.TrimLeft(operands, " \t")
	if strings.HasPrefix(trimmedOperands, `"`) {
		literal, rest, err := hpatchsyntax.DecodeQuoted(trimmedOperands)
		if err != nil {
			return targetSpec{}, "", scriptError(sourceLine, "invalid quoted target literal: "+err.Error())
		}
		target := targetSpec{
			kind: targetLiteral, literal: literal,
			count: targetCountHint(rest, finalValueFollows),
		}
		if err := validateTargetLiteral(sourceLine, literal); err != nil {
			return target, "", err
		}
		count, trailing, err := parseTargetCount(sourceLine, rest, finalValueFollows)
		target.count = count
		if err != nil {
			return target, "", err
		}
		return target, trailing, nil
	}
	token, trailing := firstToken(operands)
	if token == "" {
		return targetSpec{}, "", scriptError(sourceLine, "target must not be empty")
	}
	if startText, endText, rangeTarget := strings.Cut(token, ".."); rangeTarget {
		target := targetSpec{kind: targetRange}
		if strings.Contains(endText, "..") {
			return target, "", scriptError(sourceLine, "range target must contain exactly two rows")
		}
		start, err := parseRowReference(sourceLine, startText)
		target.start = start
		if err != nil {
			return target, "", err
		}
		end, err := parseRowReference(sourceLine, endText)
		target.end = end
		if err != nil {
			return target, "", err
		}
		return target, trailing, nil
	}

	target := targetSpec{}
	row, err := parseRowReference(sourceLine, token)
	target.start = row
	if err != nil {
		return target, "", err
	}
	target.kind = targetLine
	trimmed := strings.TrimLeft(trailing, " \t")
	if !strings.HasPrefix(trimmed, `"`) {
		return target, trailing, nil
	}
	literal, rest, err := hpatchsyntax.DecodeQuoted(trimmed)
	if err != nil {
		return target, "", scriptError(sourceLine, "invalid quoted target literal: "+err.Error())
	}
	if finalValueFollows && strings.TrimSpace(rest) == "" {
		return target, trimmed, nil
	}
	target.kind = targetText
	target.literal = literal
	target.count = targetCountHint(rest, finalValueFollows)
	if err := validateTargetLiteral(sourceLine, literal); err != nil {
		return target, "", err
	}
	count, rest, err := parseTargetCount(sourceLine, rest, finalValueFollows)
	target.count = count
	if err != nil {
		return target, "", err
	}
	return target, rest, nil
}

func validateTargetLiteral(sourceLine int, literal string) error {
	if literal == "" {
		return scriptError(sourceLine, "target literal must not be empty")
	}
	if strings.ContainsRune(literal, '\r') {
		return scriptError(sourceLine, "target literal contains a forbidden carriage return")
	}
	for _, character := range literal {
		if character < 0x20 && character != '\t' && character != '\n' {
			return scriptError(sourceLine, "target literal contains a forbidden control character")
		}
	}
	return nil
}

func targetCountHint(rest string, finalValueFollows bool) int {
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" || finalValueFollows && strings.HasPrefix(rest, `"`) {
		return 1
	}
	return 2
}

func parseTargetCount(sourceLine int, rest string, finalValueFollows bool) (int, string, error) {
	rest = strings.TrimLeft(rest, " \t")
	if targetCountHint(rest, finalValueFollows) == 1 {
		return 1, rest, nil
	}
	countText, trailing := firstToken(rest)
	if !positiveDecimalPattern.MatchString(countText) {
		return 2, "", scriptFailure(sourceLine, reasonInvalidCount, "invalid target count")
	}
	count, err := strconv.Atoi(countText)
	if err != nil {
		return 2, "", scriptFailure(sourceLine, reasonInvalidCount, "target count is out of range")
	}
	return count, trailing, nil
}

func firstToken(value string) (string, string) {
	value = strings.TrimLeft(value, " \t")
	for index := range len(value) {
		if value[index] == ' ' || value[index] == '\t' || value[index] == '\r' || value[index] == '\n' {
			return value[:index], value[index:]
		}
	}
	return value, ""
}

func parseRowReference(sourceLine int, value string) (rowReference, error) {
	reference, err := verifiedrow.ParseReference(value)
	if err != nil {
		if errors.Is(err, verifiedrow.ErrLineOutOfRange) {
			return rowReference{}, scriptError(sourceLine, "row line is out of range")
		}
		return rowReference{}, scriptError(sourceLine, fmt.Sprintf("invalid row reference %q; expected LINE:HASH", value))
	}
	line := int(reference.Line)
	if line < 1 || uint64(line) != reference.Line {
		return rowReference{}, scriptError(sourceLine, "row line is out of range")
	}
	return rowReference{line: line, hash: reference.Hash}, nil
}

func onlyOperandWhitespace(value string) bool {
	for index := range len(value) {
		if !isOperandWhitespace(value[index]) {
			return false
		}
	}
	return true
}

func isOperandWhitespace(character byte) bool {
	return character == ' ' || character == '\t' || character == '\r' || character == '\n'
}

func scriptError(line int, message string) *commandError {
	return scriptFailure(line, reasonSyntax, message)
}

func scriptFailure(line int, reason failureReason, message string) *commandError {
	return &commandError{Line: line, Reason: reason, Message: message}
}
