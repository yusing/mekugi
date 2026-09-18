package shellsyntax

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

func isProgramHeader(line string) bool {
	return strings.HasPrefix(line, "#!") && !isDirectiveCandidate(line)
}

func shellVariant(interpreter string) syntax.LangVariant {
	switch InterpreterIdentity(interpreter) {
	case "bash":
		return syntax.LangBash
	case "sh", "dash", "ash":
		return syntax.LangPOSIX
	case "ksh", "mksh":
		return syntax.LangMirBSDKorn
	case "zsh":
		return syntax.LangZsh
	default:
		return 0
	}
}

func shellProtectedLines(source string) (spans [][2]uint, variant syntax.LangVariant, through uint) {
	through = ^uint(0)
	parsed, err := Parse(source)
	if err != nil {
		// Split will report the invalid header before exposing any execution.
		return
	}
	variant = shellVariant(parsed.Interpreter[0])
	if variant == 0 {
		return
	}
	// Match the portable source-row contract without changing emitted bytes.
	body := strings.ReplaceAll(strings.ReplaceAll(parsed.Body, "\r\n", "\n"), "\r", "\n")
	tree, err := syntax.NewParser(syntax.Variant(variant)).Parse(strings.NewReader(body), "")
	headerLines := uint(physicalLine(source[:len(source)-len(parsed.Body)]) - 1)
	syntax.Walk(tree, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.Stmt, *syntax.Redirect:
			start, end := node.Pos(), node.End()
			last := end.Line()
			if end.Col() == 1 && last > 0 {
				last--
			}
			if last >= start.Line() {
				spans = append(spans, [2]uint{headerLines + start.Line(), headerLines + last})
			}
		}
		return true
	})
	if parseErr, ok := errors.AsType[syntax.ParseError](err); ok {
		through = headerLines + parseErr.Pos.Line()
		// The parser does not mark unclosed heredocs as Incomplete.
		if parseErr.Incomplete || strings.HasPrefix(parseErr.Text, "unclosed here-document ") {
			spans = append(spans, [2]uint{through, ^uint(0)})
			through = ^uint(0)
		}
	}
	slices.SortFunc(spans, func(a, b [2]uint) int {
		if a[0] < b[0] {
			return -1
		}
		if a[0] > b[0] {
			return 1
		}
		return 0
	})
	return
}

// SplitSource returns authored program slices without validating headers or
// expanding inherited directives. It is also suitable for unfinished display input.
func SplitSource(input string) []string {
	var programs []string
	var protected [][2]uint
	var variant syntax.LangVariant
	var through uint
	scanned := false
	start, offset := 0, 0
	lineNumber, startLine := uint(1), uint(1)
	for remaining := input; remaining != ""; {
		line, rest := splitFirstLine(remaining)
		next := offset + len(remaining) - len(rest)
		if offset > 0 && isProgramHeader(line) {
			if !scanned {
				protected, variant, through = shellProtectedLines(input[start:])
				for index := range protected {
					protected[index][0] += startLine - 1
					if protected[index][1] != ^uint(0) {
						protected[index][1] += startLine - 1
					}
				}
				if through != ^uint(0) {
					through += startLine - 1
				}
				scanned = true
			}
			for len(protected) > 0 && protected[0][1] < lineNumber {
				protected = protected[1:]
			}
			if len(protected) == 0 || protected[0][0] > lineNumber {
				programs = append(programs, input[start:offset])
				start = offset
				startLine = lineNumber
				nextHeader, err := Parse(line)
				// Reuse ranges across same-dialect programs already parsed in this suffix.
				scanned = err == nil && shellVariant(nextHeader.Interpreter[0]) == variant && lineNumber < through
			}
		}
		offset = next
		lineNumber++
		remaining = rest
	}
	return append(programs, input[start:])
}

// IsBatch recognizes interpreter boundaries outside incomplete shell constructs.
func IsBatch(input string) bool {
	return len(SplitSource(input)) > 1
}

// Split starts programs at column-zero interpreter headers outside shell constructs.
// Headers stay with their programs; all other source bytes remain unchanged.
// Only an omitted params directive inherits the preceding complete object.
func Split(input string) ([]string, error) {
	programs := SplitSource(input)

	var inherited map[string]any
	for index, source := range programs {
		parsed, err := Parse(source)
		if err != nil {
			return nil, fmt.Errorf("shell program %d: %w", index+1, err)
		}
		if len(programs) > 1 {
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
