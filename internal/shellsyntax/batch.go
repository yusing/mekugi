package shellsyntax

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
)

const BatchStopHeaderPrefix = "#!batch-stop="

const BatchHeaderPrefix = "#!batch="

// BatchHeader recognizes the two explicit batch policies without interpreting
// source lines. Split validates the selected separator and every program.
func BatchHeader(input string) (separator string, stopOnNonzero, ok bool) {
	first, _ := splitFirstLine(input)
	if separator, ok := strings.CutPrefix(first, BatchHeaderPrefix); ok {
		return separator, false, true
	}
	if separator, ok := strings.CutPrefix(first, BatchStopHeaderPrefix); ok {
		return separator, true, true
	}
	return "", false, false
}

// Split recognizes an explicit first-line batch header. Only exact
// separator lines delimit programs; all other source bytes remain program data.
// Only an omitted params directive inherits the preceding complete object.
func Split(input string) ([]string, error) {
	programs := []string{input}
	_, body := splitFirstLine(input)
	if separator, _, batch := BatchHeader(input); batch {
		if separator == "" || separator != strings.TrimSpace(separator) || strings.ContainsRune(separator, 0) {
			return nil, fmt.Errorf("shell batch line 1: separator must be nonempty, without surrounding whitespace or NUL")
		}
		programs = nil
		start, offset := 0, 0
		for remaining := body; remaining != ""; {
			line, rest := splitFirstLine(remaining)
			next := offset + len(remaining) - len(rest)
			if line == separator {
				programs = append(programs, body[start:offset])
				start = next
			}
			offset = next
			remaining = rest
		}
		programs = append(programs, body[start:])
		if len(programs) < 2 {
			return nil, fmt.Errorf("shell batch line 1: at least two programs separated by %q are required", separator)
		}
	}

	var inherited map[string]any
	for index, source := range programs {
		parsed, err := Parse(source)
		if err != nil {
			return nil, fmt.Errorf("shell program %d: %w", index+1, err)
		}
		if len(programs) > 1 {
			if parsed.HasScript {
				return nil, fmt.Errorf("shell program %d: line 1: #!script must be the sole directive", index+1)
			}
			if strings.TrimSpace(parsed.Body) == "" {
				return nil, fmt.Errorf("shell program %d: line 1: batch programs must have a body", index+1)
			}
		}
		if parsed.HasParams {
			inherited = parsed.Params
		} else if inherited != nil {
			params, err := json.Marshal(&inherited, json.Deterministic(true))
			if err != nil {
				return nil, err
			}
			header := "#!params=" + string(params) + "\n"
			line, rest := splitFirstLine(source)
			trimmed := trimField(line)
			if strings.HasPrefix(trimmed, "#!") && !isDirectiveCandidate(trimmed) {
				// Preserve the authored selector and its complete line terminator.
				header = source[:len(source)-len(rest)] + header
				source = rest
			}
			programs[index] = header + source
		}
	}
	return programs, nil
}
