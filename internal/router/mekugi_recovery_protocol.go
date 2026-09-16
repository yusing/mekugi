package router

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/hpatchsyntax"
)

const mekugiRecoveryDescription = `Correction of the latest rejected HPATCH/2 script. Invalid correction leaves the retained script and workspace unchanged.`

//go:embed mekugi_recovery_grammar.lark
var mekugiRecoveryGrammar string

type recoveryCommandReference struct {
	handle string

	index  int
	header int
	end    int
	source string
	parts  recoveryCommandParts
}

type recoveryCommandParts struct {
	operation string
	target    string
	value     string
	multiline bool
	parsed    bool
	identity  mekugi.TargetIdentity
}

type recoveryOperation struct {
	sequence int
	command  *recoveryCommandReference
	target   string
	value    *string
}

type recoveryEdit struct {
	sequence int
	script   string
}

func recoveryCommands(script string) []recoveryCommandReference {
	// Bind handles to the entire immutable script as well as their frame.
	// Text corrections can change a preceding path without changing a
	// mutation's bytes; a handle from that older context must not retarget it.
	scope := sha256.Sum256([]byte(script))
	lines := hpatchsyntax.SplitPhysicalLines(script)
	offsets := make([]int, len(lines)+1)
	for index, line := range lines {
		offsets[index+1] = offsets[index] + len(line.Text) + len(line.Terminator)
	}
	commands := make([]recoveryCommandReference, 0)
	for index := 0; index < len(lines); {
		header := index
		line := lines[index].Text
		index++
		if strings.TrimSpace(line) == "" {
			continue
		}
		frame, _ := hpatchsyntax.FrameCommand(lines, header, line)
		index = max(frame.Next, index)
		source := script[offsets[header]:offsets[index]]
		commands = append(commands, recoveryCommandReference{
			handle: fmt.Sprintf("C%d:%s", len(commands)+1, recoveryHash(string(scope[:])+source)),
			index:  len(commands) + 1,
			header: header,
			end:    index,
			source: source,
			parts:  recoveryCommandPartsOf(line, frame),
		})
	}
	return commands
}

// recoveryCommandPartsOf parses a command header and frame into recovery command parts.
func recoveryCommandPartsOf(header string, frame hpatchsyntax.CommandFrame) recoveryCommandParts {
	operation, operands := recoveryToken(header)
	if operation != "type" && operation != "add" {
		return recoveryCommandParts{}
	}
	if frame.Marker != "" {
		target := strings.TrimSpace(strings.TrimSuffix(operands, frame.Marker))
		parts := recoveryCommandParts{
			operation: operation,
			target:    target,
			value:     frame.Body,
			multiline: true,
		}
		if operation == "type" && target == "" || operation == "add" && target == "EOF" {
			parts.parsed = true
			return parts
		}
		identity, trailing, err := mekugi.ParseTargetIdentity(target, false)
		if err == nil && strings.TrimSpace(trailing) == "" {
			parts.parsed = true
			parts.identity = identity
		}
		return parts
	}
	if operation == "type" && strings.HasPrefix(strings.TrimSpace(operands), `"`) {
		value, trailing, err := hpatchsyntax.DecodeQuoted(strings.TrimSpace(operands))
		if err == nil && strings.TrimSpace(trailing) == "" {
			return recoveryCommandParts{operation: operation, value: value, parsed: true}
		}
	}
	if operation == "add" {
		destination, trailing := recoveryToken(operands)
		if destination == "EOF" {
			value, rest, err := hpatchsyntax.DecodeQuoted(trailing)
			if err == nil && strings.TrimSpace(rest) == "" {
				return recoveryCommandParts{
					operation: operation,
					target:    destination,
					value:     value,
					parsed:    true,
				}
			}
			return recoveryCommandParts{operation: operation}
		}
	}
	identity, trailing, err := mekugi.ParseTargetIdentity(operands, true)
	if err != nil {
		return recoveryCommandParts{operation: operation}
	}
	target := strings.TrimSpace(operands[:len(operands)-len(trailing)])
	value, rest, err := hpatchsyntax.DecodeQuoted(trailing)
	if err != nil || strings.TrimSpace(rest) != "" {
		return recoveryCommandParts{operation: operation}
	}
	return recoveryCommandParts{
		operation: operation,
		target:    target,
		value:     value,
		parsed:    true,
		identity:  identity,
	}
}

type recoveredScript struct {
	script string
	delta  string
}

func recoverScriptDetailed(ctx context.Context, rejectedScript, payload string) (recoveredScript, error) {
	if ctx == nil {
		return recoveredScript{}, fmt.Errorf("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return recoveredScript{}, err
	}
	fields := strings.Fields(payload)
	if len(fields) != 0 && (fields[0] == "type" || fields[0] == "add") {
		rebuilt, err := mekugi.EditTextBounded(ctx, rejectedScript, payload, maxMekugiScriptBytes)
		if err != nil {
			return recoveredScript{}, err
		}
		if rebuilt == rejectedScript {
			return recoveredScript{}, fmt.Errorf("correction must change the retained script")
		}
		if strings.TrimSpace(rebuilt) == "" {
			return recoveredScript{}, fmt.Errorf("correction must leave a nonempty script")
		}
		return recoveredScript{script: rebuilt, delta: "Updated retained-script text with ordinary HPATCH mutations."}, nil
	}
	commands := recoveryCommands(rejectedScript)
	operations, err := parseRecoveryPayload(commands, payload)
	if err != nil {
		return recoveredScript{}, err
	}
	edits, err := planRecoveryEdits(rejectedScript, operations)
	if err != nil {
		return recoveredScript{}, err
	}
	var editScript strings.Builder
	for _, edit := range edits {
		editScript.WriteString(edit.script)
		editScript.WriteByte('\n')
	}
	rebuilt, err := mekugi.EditTextBounded(ctx, rejectedScript, editScript.String(), maxMekugiScriptBytes)
	if err != nil {
		return recoveredScript{}, err
	}
	return recoveredScript{script: rebuilt, delta: formatRecoveryDelta(operations)}, nil
}

func formatRecoveryDelta(operations []recoveryOperation) string {
	var delta strings.Builder
	for _, operation := range operations {
		if operation.value != nil {
			fmt.Fprintf(&delta, "%s: replaced command value (%d bytes)\n", operation.command.handle, len(*operation.value))
			continue
		}
		fmt.Fprintf(
			&delta,
			"%s: %s -> %s\n",
			operation.command.handle,
			operation.command.parts.target,
			operation.target,
		)
	}
	return strings.TrimSuffix(delta.String(), "\n")
}

func parseRecoveryPayload(
	commands []recoveryCommandReference,
	payload string,
) ([]recoveryOperation, error) {
	lines := hpatchsyntax.SplitPhysicalLines(payload)
	operations := make([]recoveryOperation, 0)
	for index := 0; index < len(lines); {
		lineNumber := index + 1
		line := lines[index]
		index++
		if strings.TrimSpace(line.Text) == "" {
			continue
		}
		handle, operand, ok := strings.Cut(line.Text, " ")
		if !ok || handle == "" || operand == "" || strings.HasPrefix(operand, " ") ||
			operand != strings.TrimRight(operand, " \t") {
			return nil, recoveryError(lineNumber, "expected a command handle and a target or value correction")
		}
		command, err := resolveRecoveryCommand(commands, handle)
		if err != nil {
			return nil, recoveryError(lineNumber, err.Error())
		}
		if valueSource, isValue := strings.CutPrefix(operand, "value "); isValue {
			if !command.parts.parsed {
				return nil, recoveryError(lineNumber, "value correction requires a parsed type or add command")
			}
			header := "type " + valueSource
			frame, err := hpatchsyntax.FrameCommand(lines, lineNumber-1, header)
			if err != nil {
				return nil, recoveryError(lineNumber, err.Error())
			}
			parts := recoveryCommandPartsOf(header, frame)
			if !parts.parsed || parts.target != "" {
				return nil, recoveryError(lineNumber, "expected one quoted or heredoc value")
			}
			if parts.value == command.parts.value {
				return nil, recoveryError(lineNumber, "replacement value must differ from the rejected value")
			}
			index = frame.Next
			operations = append(operations, recoveryOperation{
				sequence: len(operations) + 1, command: command, value: new(parts.value),
			})
			continue
		}
		target := strings.TrimPrefix(operand, "target ")
		replacementTarget, trailing, targetErr := mekugi.ParseTargetIdentity(target, false)
		if !command.parts.parsed || command.parts.target == "" || command.parts.target == "EOF" ||
			targetErr != nil || strings.TrimSpace(trailing) != "" || target == "EOF" ||
			!hpatchsyntax.ValidOperandSpacing(target) {
			return nil, recoveryError(lineNumber, "command must be target-bearing and the replacement target must be valid")
		}
		if replacementTarget == command.parts.identity {
			return nil, recoveryError(lineNumber, "replacement target must differ from the rejected target")
		}
		operations = append(operations, recoveryOperation{
			sequence: len(operations) + 1,
			command:  command,
			target:   target,
		})
	}
	if len(operations) == 0 {
		return nil, recoveryError(1, "recovery payload must contain at least one correction")
	}
	return operations, nil
}

func resolveRecoveryCommand(
	commands []recoveryCommandReference,
	handle string,
) (*recoveryCommandReference, error) {
	if len(handle) < 7 || handle[0] != 'C' {
		return nil, fmt.Errorf("invalid command handle %q", handle)
	}
	indexText, hash, ok := strings.Cut(handle[1:], ":")
	if !ok || len(hash) != base64.RawURLEncoding.EncodedLen(sha256.Size) || !recoveryPositiveDecimal(indexText) {
		return nil, fmt.Errorf("invalid command handle %q", handle)
	}
	if decoded, err := base64.RawURLEncoding.Strict().DecodeString(hash); err != nil || len(decoded) != sha256.Size {
		return nil, fmt.Errorf("invalid command handle %q", handle)
	}
	index, err := strconv.Atoi(indexText)
	if err != nil || index > len(commands) {
		return nil, fmt.Errorf("command handle %q is stale or unavailable", handle)
	}
	command := &commands[index-1]
	if command.handle != handle {
		return nil, fmt.Errorf("command handle %q is stale; latest handle is %s", handle, command.handle)
	}
	return command, nil
}

func planRecoveryEdits(script string, operations []recoveryOperation) ([]recoveryEdit, error) {
	seen := make(map[int]struct{}, len(operations))
	lines := hpatchsyntax.SplitPhysicalLines(script)
	logicalRows := mekugiLogicalRowsByPhysicalLine(script, lines)
	edits := make([]recoveryEdit, 0, len(operations))
	for _, operation := range operations {
		if _, duplicate := seen[operation.command.index]; duplicate {
			return nil, recoveryError(operation.sequence, "duplicate target correction for one command")
		}
		seen[operation.command.index] = struct{}{}
		commandTarget, err := recoveryPhysicalTarget(
			script,
			logicalRows,
			operation.command.header,
			operation.command.end-1,
		)
		if err != nil {
			return nil, recoveryError(operation.sequence, err.Error())
		}
		var replacement string
		if operation.value != nil {
			header := operation.command.parts.operation
			if target := operation.command.parts.target; target != "" {
				header += " " + target
			}
			replacement = header + " " + string(mustMarshalJSON(*operation.value)) + recoveryTerminatorSuffix(operation.command.source)
		} else {
			replacement = renderRecoveryMutation(operation.command, operation.target)
		}
		edits = append(edits, recoveryEdit{
			sequence: operation.sequence,
			script:   "type " + commandTarget + " " + string(mustMarshalJSON(replacement)),
		})
	}
	return edits, nil
}

func renderRecoveryMutation(command *recoveryCommandReference, target string) string {
	header := command.parts.operation + " " + target
	if command.parts.multiline {
		originalHeader := hpatchsyntax.SplitPhysicalLines(command.source)[0].Text
		_, marker, _ := strings.CutLast(originalHeader, " ")
		// Retarget only the header. Preserve the value's framing mode and
		// physical bytes, including mixed terminators and an empty body.
		return header + " " + marker + command.source[len(originalHeader):]
	}
	return header + " " + string(mustMarshalJSON(command.parts.value)) + recoveryTerminatorSuffix(command.source)
}

func recoveryPhysicalTarget(script string, logicalRows [][]int, start, end int) (string, error) {
	if start < 0 || end >= len(logicalRows) || start > end {
		return "", fmt.Errorf("physical target is unavailable")
	}
	for start <= end && len(logicalRows[start]) == 0 {
		start++
	}
	for end >= start && len(logicalRows[end]) == 0 {
		end--
	}
	if start > end {
		return "", fmt.Errorf("physical target has no logical row")
	}
	first := recoveryLogicalHandle(script, logicalRows[start][0])
	if start == end && len(logicalRows[start]) == 1 {
		return first, nil
	}
	lastRows := logicalRows[end]
	return first + ".." + recoveryLogicalHandle(script, lastRows[len(lastRows)-1]), nil
}

func recoveryLogicalHandle(script string, row int) string {
	reference := mekugi.TextReferences(script, row)
	handle, _ := recoveryToken(reference)
	return handle
}

func recoveryPositiveDecimal(value string) bool {
	if value == "" || value[0] < '1' || value[0] > '9' {
		return false
	}
	for _, character := range value[1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func recoveryToken(value string) (string, string) {
	value = strings.TrimLeft(value, " \t")
	for index, character := range value {
		if character == ' ' || character == '\t' || character == '\r' || character == '\n' {
			return value[:index], value[index:]
		}
	}
	return value, ""
}

func recoveryHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func recoveryTerminatorSuffix(value string) string {
	switch {
	case strings.HasSuffix(value, "\r\n"):
		return "\r\n"
	case strings.HasSuffix(value, "\n"):
		return "\n"
	case strings.HasSuffix(value, "\r"):
		return "\r"
	default:
		return ""
	}
}

func recoveryError(line int, message string) error {
	return fmt.Errorf("recovery operation at line %d: %s", line, message)
}
