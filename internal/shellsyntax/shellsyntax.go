package shellsyntax

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Parsed is the portable shell header result.
type Parsed struct {
	Interpreter     []string       `json:"interpreter,omitempty"`
	Body            string         `json:"body,omitempty"`
	CommandTemplate string         `json:"commandTemplate,omitempty"`
	Params          map[string]any `json:"params"`
	ParamsLine      int            `json:"paramsLine,omitempty"`
	HasParams       bool           `json:"hasParams,omitzero"`
	ScriptPath      string         `json:"scriptPath,omitempty"`
	HasScript       bool           `json:"hasScript,omitzero"`
}

// HeaderError locates a rejected header in the submitted program. Line is
// one-based and counts CRLF as one line terminator, just like header parsing.
type HeaderError struct {
	Line int
	Err  error
}

func (e *HeaderError) Error() string { return fmt.Sprintf("line %d: %v", e.Line, e.Err) }
func (e *HeaderError) Unwrap() error { return e.Err }

// Parse reads the portable shell header. A retained-script path is returned to
// the host without reading it; filesystem resolution remains host-owned.
func Parse(input string) (Parsed, error) {
	if strings.ContainsRune(input, 0) {
		return Parsed{}, &HeaderError{Line: physicalLine(input[:strings.IndexByte(input, 0)]), Err: errors.New("script must not contain a NUL byte")}
	}

	retainedLine, retainedBody := splitFirstLine(input)
	retainedLine = trimField(retainedLine)
	if path, ok := strings.CutPrefix(retainedLine, "#!script="); ok {
		if retainedBody != "" {
			return Parsed{}, &HeaderError{Line: 2, Err: errors.New("#!script must be the sole directive")}
		}
		return Parsed{ScriptPath: path, HasScript: true}, nil
	}

	interpreter := []string{"bash"}
	body := input
	firstLine, firstBody := splitFirstLine(input)
	trimmed := trimField(firstLine)
	if strings.HasPrefix(trimmed, "#!") && !isDirectiveCandidate(trimmed) {
		selector := trimField(strings.TrimPrefix(trimmed, "#!"))
		if selector == "" {
			return Parsed{}, &HeaderError{Line: 1, Err: errors.New("shebang must select an interpreter")}
		}
		interpreter = splitFields(selector)
		if interpreter[0] == "env" || interpreter[0] == "/usr/bin/env" {
			interpreter = interpreter[1:]
			if len(interpreter) != 0 && interpreter[0] == "-S" {
				interpreter = interpreter[1:]
			}
			if len(interpreter) == 0 || strings.HasPrefix(interpreter[0], "-") {
				return Parsed{}, &HeaderError{Line: 1, Err: errors.New("env shebang must select an interpreter")}
			}
		}
		body = firstBody
	}

	headerOffset := physicalLine(input[:len(input)-len(body)]) - 1
	commandTemplate, params, hasParams, paramsLine, body, err := parseDirectives(body)
	if err != nil {
		if located, ok := errors.AsType[*HeaderError](err); ok {
			located.Line += headerOffset
		}
		return Parsed{}, err
	}

	if hasParams {
		paramsLine += headerOffset
	}
	return Parsed{
		Interpreter:     interpreter,
		Body:            body,
		CommandTemplate: commandTemplate,
		Params:          params,
		ParamsLine:      paramsLine,
		HasParams:       hasParams,
	}, nil
}

// InterpreterIdentity normalizes an interpreter path for policy comparisons.
func InterpreterIdentity(interpreter string) string {
	base := filepath.Base(strings.ReplaceAll(interpreter, "\\", "/"))
	base = strings.TrimSuffix(strings.ToLower(base), ".exe")
	return base
}

// parseDirectives extracts #!cmd and #!params directives from shell script header lines.
func parseDirectives(input string) (commandTemplate string, params map[string]any, hasParams bool, paramsLine int, body string, err error) {
	lineNumber := 1
	defer func() {
		if err != nil {
			err = &HeaderError{Line: lineNumber, Err: err}
		}
	}()
	remaining := input
	seen := make(map[string]struct{}, 2)
	for remaining != "" {
		line, rest := splitFirstLine(remaining)
		trimmed := trimField(line)
		key, value, ok := parseDirectiveLine(trimmed)
		if !ok {
			if malformedDirective(trimmed) || strings.HasPrefix(trimmed, "!") {
				return "", nil, false, 0, "", errors.New("shell directive must use #!{key}={value}")
			}
			break
		}
		if key != "cmd" && key != "params" {
			return "", nil, false, 0, "", fmt.Errorf("unsupported shell directive #!%s", key)
		}
		if _, duplicate := seen[key]; duplicate {
			return "", nil, false, 0, "", fmt.Errorf("shell directive #!%s must not occur more than once", key)
		}
		seen[key] = struct{}{}

		switch key {
		case "cmd":
			if value == "" {
				return "", nil, false, 0, "", errors.New("command template must not be empty")
			}
			if strings.Count(value, "{.}") != 1 {
				return "", nil, false, 0, "", errors.New("command template must contain exactly one {.} placeholder")
			}
			commandTemplate = value
		case "params":
			if err := json.Unmarshal([]byte(value), &params); err != nil {
				return "", nil, false, 0, "", fmt.Errorf("#!params must contain a JSON object: %w", err)
			}
			if params == nil {
				return "", nil, false, 0, "", errors.New("#!params must contain a JSON object")
			}
			paramsLine = lineNumber
			hasParams = true
		}
		remaining = rest
		lineNumber++
	}
	return commandTemplate, params, hasParams, paramsLine, remaining, nil
}

// parseDirectiveLine parses one shell directive line into its key and value components.
func parseDirectiveLine(line string) (key, value string, ok bool) {
	if rest, matched := strings.CutPrefix(line, "#!"); matched {
		separator := strings.IndexByte(rest, '=')
		if separator > 0 && validDirectiveKey(rest[:separator]) {
			return rest[:separator], rest[separator+1:], true
		}
	}
	if rest, matched := strings.CutPrefix(line, "#"); matched {
		rest = strings.TrimLeft(rest, " \t")
		if rest, matched = strings.CutPrefix(rest, "!params"); matched &&
			(rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			return "params", strings.TrimLeft(rest, " \t"), true
		}
	}
	return "", "", false
}

// validDirectiveKey reports whether value is a valid shell directive key name.
func validDirectiveKey(value string) bool {
	if value == "" {
		return false
	}
	first := value[0]
	if !('A' <= first && first <= 'Z') && !('a' <= first && first <= 'z') {
		return false
	}
	for _, character := range value[1:] {
		if character != '-' && character != '_' &&
			(character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') &&
			(character < '0' || character > '9') {
			return false
		}
	}
	return true
}

// isDirectiveCandidate reports whether line could be a well-formed or malformed directive.
func isDirectiveCandidate(line string) bool {
	_, _, ok := parseDirectiveLine(line)
	return ok || malformedDirective(line)
}

// malformedDirective reports whether line contains a recognized but malformed directive.
func malformedDirective(line string) bool {
	for _, name := range []string{"#!cmd", "#!params"} {
		if rest, ok := strings.CutPrefix(line, name); ok &&
			(rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			return true
		}
	}
	return false
}

func physicalLine(prefix string) int {
	lineNumber := 1
	for index := 0; index < len(prefix); index++ {
		switch prefix[index] {
		case '\r':
			if index+1 < len(prefix) && prefix[index+1] == '\n' {
				index++
			}
			lineNumber++
		case '\n':
			lineNumber++
		}
	}
	return lineNumber
}

// splitFirstLine splits input into its first physical line and remaining body.
func splitFirstLine(input string) (line, body string) {
	for index := range len(input) {
		if input[index] != '\r' && input[index] != '\n' {
			continue
		}
		end := index + 1
		if input[index] == '\r' && end < len(input) && input[end] == '\n' {
			end++
		}
		return input[:index], input[end:]
	}
	return input, ""
}

// trimField removes leading and trailing spaces and tabs from value.
func trimField(value string) string {
	return strings.Trim(value, " \t")
}

// splitFields splits value on spaces and tabs into non-empty fields.
func splitFields(value string) []string {
	return strings.FieldsFunc(value, func(character rune) bool {
		return character == ' ' || character == '\t'
	})
}
