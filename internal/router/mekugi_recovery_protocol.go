package router

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/hpatchsyntax"
)

type recoveryCommandReference struct {
	handle string
	path   string
	script int

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

// Keep the full integrity binding private. Length framing separates the script
// from the ordered handle mapping, including when source text contains controls.
func recoveryHandlesBinding(script string, handles []string) string {
	return recoveryHash(strconv.Itoa(len(script)) + ":" + script + strings.Join(handles, ","))
}

func recoveryCommands(script string, handles []string) []recoveryCommandReference {
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
		var handle string
		if len(commands) < len(handles) {
			handle = handles[len(commands)]
		}
		commands = append(commands, recoveryCommandReference{
			handle: handle,
			index:  len(commands) + 1,
			header: header,
			end:    index,
			source: source,
			parts:  recoveryCommandPartsOf(line, frame),
		})
	}
	return commands
}

// recoveryCommandPartsOf parses one pathless script command and frame.
func recoveryCommandPartsOf(header string, frame hpatchsyntax.CommandFrame) recoveryCommandParts {
	operation, operands := recoveryToken(header)
	if operation != "type" && operation != "add" && operation != "append" {
		return recoveryCommandParts{}
	}
	if frame.Marker != "" {
		operands = strings.TrimSpace(strings.TrimSuffix(operands, frame.Marker))
		if operation == "append" {
			if operands != "" {
				return recoveryCommandParts{operation: operation}
			}
			return recoveryCommandParts{operation: operation, value: frame.Body, multiline: true, parsed: true}
		}
		if operation == "add" && operands == "EOF" {
			return recoveryCommandParts{
				operation: operation, target: "EOF", value: frame.Body,
				multiline: true, parsed: true,
			}
		}
		identity, trailing, err := mekugi.ParseTargetIdentity(operands, false)
		if err != nil || strings.TrimSpace(trailing) != "" {
			return recoveryCommandParts{operation: operation}
		}
		target := strings.TrimSpace(operands[:len(operands)-len(trailing)])
		return recoveryCommandParts{
			operation: operation, target: target, value: frame.Body,
			multiline: true, parsed: true, identity: identity,
		}
	}
	if operation == "append" {
		value, trailing, err := hpatchsyntax.DecodeQuoted(strings.TrimSpace(operands))
		if err != nil || strings.TrimSpace(trailing) != "" {
			return recoveryCommandParts{operation: operation}
		}
		return recoveryCommandParts{operation: operation, value: value, parsed: true}
	}
	if operation == "add" && strings.HasPrefix(operands, "EOF ") {
		value, rest, err := hpatchsyntax.DecodeQuoted(strings.TrimSpace(strings.TrimPrefix(operands, "EOF ")))
		if err != nil || strings.TrimSpace(rest) != "" {
			return recoveryCommandParts{operation: operation, target: "EOF"}
		}
		return recoveryCommandParts{operation: operation, target: "EOF", value: value, parsed: true}
	}
	identity, trailing, err := mekugi.ParseTargetIdentity(operands, true)
	if err != nil {
		return recoveryCommandParts{operation: operation}
	}
	target := strings.TrimSpace(operands[:len(operands)-len(trailing)])
	normalized, value, identity, err := mekugi.ParseInlineMutation(header)
	if err != nil {
		return recoveryCommandParts{operation: operation, target: target}
	}
	return recoveryCommandParts{
		operation: operation, target: normalized, value: value, parsed: true, identity: identity,
	}
}

// recoveryValueParts decodes a pathless recovery value correction.
func recoveryValueParts(valueLine string, frame hpatchsyntax.CommandFrame) (string, bool) {
	if frame.Marker != "" {
		operands := strings.TrimSpace(strings.TrimSuffix(valueLine, frame.Marker))
		if operands != "" {
			return "", false
		}
		return frame.Body, true
	}
	value, trailing, err := hpatchsyntax.DecodeQuoted(strings.TrimSpace(valueLine))
	if err != nil || strings.TrimSpace(trailing) != "" {
		return "", false
	}
	return value, true
}

type recoveredScript struct {
	script string
	delta  string
}

func recoverScriptDetailed(ctx context.Context, rejectedScript, payload string, handles []string) (recoveredScript, error) {
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
	commands := recoveryCommands(rejectedScript, handles)
	if len(handles) != len(commands) {
		return recoveredScript{}, fmt.Errorf("invalid retained command handles")
	}
	seen := make(map[string]bool, len(commands))
	for _, command := range commands {
		if _, ok := parseShortHandle(command.handle); !ok || seen[command.handle] {
			return recoveredScript{}, fmt.Errorf("invalid retained command handles")
		}
		seen[command.handle] = true
	}
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
			(operand != strings.TrimRight(operand, " \t") && !strings.HasPrefix(operand, "value <<")) {
			return nil, recoveryError(lineNumber, "expected a command handle and a target or value correction")
		}
		command, err := resolveRecoveryCommand(commands, handle)
		if err != nil {
			return nil, recoveryError(lineNumber, err.Error())
		}
		if valueSource, isValue := strings.CutPrefix(operand, "value "); isValue {
			if !command.parts.parsed {
				return nil, recoveryError(lineNumber, "value correction requires a parsed type, add, or append command")
			}
			header := "type " + valueSource
			frame, err := hpatchsyntax.FrameCommand(lines, lineNumber-1, header)
			if err != nil {
				return nil, recoveryError(lineNumber, err.Error())
			}
			value, ok := recoveryValueParts(valueSource, frame)
			if !ok {
				return nil, recoveryError(lineNumber, "expected one quoted or heredoc value")
			}
			if value == command.parts.value {
				return nil, recoveryError(lineNumber, "replacement value must differ from the rejected value")
			}
			index = frame.Next
			operations = append(operations, recoveryOperation{
				sequence: len(operations) + 1, command: command, value: new(value),
			})
			continue
		}
		target := strings.TrimPrefix(operand, "target ")
		replacementTarget, trailing, targetErr := mekugi.ParseTargetIdentity(target, false)
		if !command.parts.parsed || command.parts.target == "" || command.parts.target == "EOF" ||
			command.parts.operation == "append" || targetErr != nil || strings.TrimSpace(trailing) != "" || target == "EOF" ||
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

func resolveRecoveryCommand(commands []recoveryCommandReference, handle string) (*recoveryCommandReference, error) {
	if _, ok := parseShortHandle(handle); !ok {
		return nil, fmt.Errorf("invalid command handle %q", handle)
	}
	for index := range commands {
		if commands[index].handle == handle {
			return &commands[index], nil
		}
	}
	return nil, fmt.Errorf("command handle %q is stale or unavailable", handle)
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
