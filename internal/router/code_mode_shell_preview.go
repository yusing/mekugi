package router

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// codeModeShellCall is a literal exec_command argument object read from an
// unfinished Code Mode stream. DynamicWorkdir marks a workdir that is present
// but not yet a complete literal, so relative targets are not resolvable.
type codeModeShellCall struct {
	cmd, workdir   string
	dynamicWorkdir bool
}

// Read literal cmd strings from an unfinished Code Mode stream for display
// only. This scanner does not validate or execute JS; completed calls still
// use the syntax-tree recognizer for operation evidence.
func codeModeShellFragments(source string) []codeModeShellCall {
	if len(source) > maxMekugiScriptBytes {
		return nil
	}
	regexRanges, staticObjects, callees := codeModePreviewSyntax(source)
	regexIndex := 0
	var calls []codeModeShellCall
	for at := 0; at < len(source); {
		for regexIndex < len(regexRanges) && at >= regexRanges[regexIndex].end {
			regexIndex++
		}
		if regexIndex < len(regexRanges) && at >= regexRanges[regexIndex].start {
			at = regexRanges[regexIndex].end
			continue
		}
		if next := codeModeSkipLiteral(source, at); next > at {
			at = next
			continue
		}
		if !callees[at] &&
			(!codeModeIdentifierAt(source, at, "tools") || !codeModeBareToolsCandidate(source, at)) {
			at++
			continue
		}
		next := codeModeSpace(source, at+len("tools"))
		if next >= len(source) || source[next] != '.' {
			at++
			continue
		}
		next = codeModeSpace(source, next+1)
		if !codeModeIdentifierAt(source, next, nativeExecCommandToolName) {
			at++
			continue
		}
		next = codeModeSpace(source, next+len(nativeExecCommandToolName))
		if next >= len(source) || source[next] != '(' {
			at++
			continue
		}
		next = codeModeSpace(source, next+1)
		if next >= len(source) || source[next] != '{' {
			at++
			continue
		}
		call, end, closed := codeModeShellObject(source, next)
		if closed {
			// Once the argument object is closed, trust its parsed static value,
			// not a lexical prefix that a computed key or spread may override.
			literal, found := staticObjects[next]
			switch {
			case !found || literal.end != end:
				call = codeModeShellCall{}
			case literal.call.cmd == "" && call.dynamicWorkdir:
				// A computed workdir keeps the call recognizable as an edit
				// whose targets cannot be resolved.
				call.workdir = ""
			default:
				call = literal.call
			}
		}
		if call.cmd != "" {
			calls = append(calls, call)
		}
		at = max(at+1, end)
	}
	return calls
}

type codeModeSourceRange struct{ start, end int }
type codeModeStaticObject struct {
	end  int
	call codeModeShellCall
}

// JavaScript regex literals can contain text resembling a tool invocation.
// Tree-sitter identifies their lexical ranges even when later stream input is
// unfinished. It also validates closed argument objects before their literal
// command can be shown as a stable Bash preview.
func codeModePreviewSyntax(source string) ([]codeModeSourceRange, map[int]codeModeStaticObject, map[int]bool) {
	parser := sitter.NewParser()
	defer parser.Close()
	if parser.SetLanguage(codeModeJavaScriptLanguage) != nil {
		return nil, nil, nil
	}
	bytes := []byte(source)
	tree := parser.Parse(bytes, nil)
	if tree == nil {
		return nil, nil, nil
	}
	defer tree.Close()
	var ranges []codeModeSourceRange
	objects := make(map[int]codeModeStaticObject)
	callees := make(map[int]bool)
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node.Kind() == "regex" {
			ranges = append(ranges, codeModeSourceRange{int(node.StartByte()), int(node.EndByte())})
			return
		}
		if node.Kind() == "member_expression" && toolActivityMemberPath(node, bytes, "tools", nativeExecCommandToolName) {
			callees[int(node.StartByte())] = true
		}
		if node.Kind() == "object" {
			end := int(node.EndByte())
			if end > int(node.StartByte()) && end <= len(source) && source[end-1] == '}' {
				var call codeModeShellCall
				if value, ok := toolActivityStaticJavaScriptValue(node, bytes); ok {
					if properties, ok := value.(map[string]any); ok {
						call.cmd, _ = properties["cmd"].(string)
						if workdir, exists := properties["workdir"]; exists {
							call.workdir, ok = workdir.(string)
							call.dynamicWorkdir = !ok
						}
					}
				}
				objects[int(node.StartByte())] = codeModeStaticObject{end: end, call: call}
			}
		}
		for i := range node.NamedChildCount() {
			walk(node.NamedChild(uint(i)))
		}
	}
	walk(tree.RootNode())
	return ranges, objects, callees
}

// codeModeShellObject scans an argument object from its brace. Closed
// reports that the object ended; otherwise end is len(source), which may
// itself follow a brace inside the unfinished command text. JSON arguments
// are a subset of this object syntax.
func codeModeShellObject(source string, start int) (call codeModeShellCall, end int, closed bool) {
	script := ""
	depth, brackets, parens := 1, 0, 0
	expectKey := true
	for at := start + 1; at < len(source); {
		if next := codeModeSkipComment(source, at); next > at {
			at = next
			continue
		}
		if depth == 1 && brackets == 0 && parens == 0 && expectKey {
			key, end := codeModePropertyKey(source, at)
			if key == "" {
				if source[at] != ' ' && source[at] != '\t' && source[at] != '\r' && source[at] != '\n' && source[at] != ',' {
					script = "" // An unparsed property could override cmd.
				}
			} else if colon := codeModeTrivia(source, end); key == "workdir" && colon < len(source) && source[colon] == ':' {
				expectKey = false
				call.workdir, call.dynamicWorkdir = "", true
				valueStart := codeModeTrivia(source, colon+1)
				if valueStart < len(source) && (source[valueStart] == '"' || source[valueStart] == '\'') {
					value, consumed := toolActivityJavaScriptStringFragment(source[valueStart:])
					at = valueStart + max(1, consumed)
					if consumed > 1 && source[at-1] == source[valueStart] {
						// Only a finished literal can resolve relative targets.
						if follow := codeModeTrivia(source, at); follow >= len(source) || source[follow] == ',' || source[follow] == '}' {
							call.workdir, call.dynamicWorkdir = value, false
						}
					}
					continue
				}
			} else if key != "cmd" {
				follow := codeModeTrivia(source, end)
				if follow >= len(source) && codeModeIdentifierByte(source[at]) &&
					!slices.ContainsFunc([]string{"cmd", "get", "set", "async"}, func(name string) bool { return strings.HasPrefix(name, key) }) {
					// A name still arriving that cannot become cmd or an
					// accessor prefix cannot replace cmd, whatever follows it.
				} else if follow >= len(source) || source[follow] != ':' {
					script = "" // A method/getter or unfinished property is not proven safe.
				} else {
					expectKey = false
				}
			}
			if key == "cmd" {
				valueStart := codeModeSpace(source, end)
				if valueStart < len(source) && source[valueStart] == ':' {
					script = "" // A later cmd property replaces an earlier one.
					expectKey = false
					valueStart = codeModeTrivia(source, valueStart+1)
					if valueStart < len(source) && (source[valueStart] == '"' || source[valueStart] == '\'') {
						value, consumed := toolActivityJavaScriptStringFragment(source[valueStart:])
						at = valueStart + max(1, consumed)
						script = value
						if at <= len(source) && at > valueStart && source[at-1] == source[valueStart] {
							follow := codeModeTrivia(source, at)
							if follow < len(source) && source[follow] != ',' && source[follow] != '}' {
								script = "" // A larger expression is not a literal cmd.
							}
						}
						continue
					}
				}
				if key == "cmd" {
					// A shorthand property can replace an earlier literal cmd.
					script = ""
				}
			}
			if source[at] == '.' && strings.HasPrefix(source[at:], "...") {
				script = "" // A following object spread may replace cmd.
			}
		}
		if next := codeModeSkipString(source, at); next > at {
			at = next
			continue
		}
		switch source[at] {
		case '{':
			depth++
			expectKey = false
		case '}':
			depth--
			if depth == 0 {
				call.cmd = script
				return call, at + 1, true
			}
		case '[':
			brackets++
		case ']':
			brackets--
		case '(':
			parens++
		case ')':
			parens--
		case ':':
			if depth == 1 && brackets == 0 && parens == 0 {
				expectKey = false
			}
		case ',':
			if depth == 1 && brackets == 0 && parens == 0 {
				expectKey = true
			}
		}
		at++
	}
	call.cmd = script
	return call, len(source), false
}

func codeModePropertyKey(source string, at int) (string, int) {
	if at >= len(source) {
		return "", at
	}
	if source[at] == '"' || source[at] == '\'' {
		value, consumed := toolActivityJavaScriptStringFragment(source[at:])
		return value, at + max(1, consumed)
	}
	if !codeModeIdentifierByte(source[at]) {
		return "", at
	}
	end := at + 1
	for end < len(source) && codeModeIdentifierByte(source[end]) {
		end++
	}
	return source[at:end], end
}

func codeModeIdentifierAt(source string, at int, name string) bool {
	if at < 0 || at+len(name) > len(source) || source[at:at+len(name)] != name {
		return false
	}
	if at > 0 {
		previous, _ := utf8.DecodeLastRuneInString(source[:at])
		if codeModeIdentifierRune(previous) {
			return false
		}
	}
	if at+len(name) < len(source) {
		next, _ := utf8.DecodeRuneInString(source[at+len(name):])
		if codeModeIdentifierRune(next) {
			return false
		}
	}
	return true
}

func codeModeIdentifierRune(char rune) bool {
	if char < utf8.RuneSelf {
		return codeModeIdentifierByte(byte(char))
	}
	// The live preview must not guess the full ECMAScript IdentifierPart set.
	// A non-space non-ASCII neighbor is ambiguous until the syntax tree proves
	// the callee, so reject the lexical fallback conservatively.
	return !unicode.IsSpace(char)
}

// A recovered parse may not retain a callee node until an argument object
// closes. In that interval, admit only a bare lexical tools root, never the
// tools property of another object.
func codeModeBareToolsCandidate(source string, at int) bool {
	var previous byte
	for cursor := 0; cursor < at; {
		if next := codeModeSkipComment(source, cursor); next > cursor {
			cursor = next
			continue
		}
		if next := codeModeSkipString(source, cursor); next > cursor {
			previous = source[cursor]
			cursor = next
			continue
		}
		char, size := utf8.DecodeRuneInString(source[cursor:at])
		if !unicode.IsSpace(char) {
			if char >= utf8.RuneSelf {
				previous = 0xff
			} else {
				previous = source[cursor]
			}
		}
		cursor += size
	}
	return previous != '.' && previous != 0xff
}

func codeModeIdentifierByte(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
		char >= '0' && char <= '9' || char == '_' || char == '$'
}

func codeModeSpace(source string, at int) int {
	for at < len(source) {
		char, size := utf8.DecodeRuneInString(source[at:])
		if !unicode.IsSpace(char) && char != '\ufeff' {
			break
		}
		at += size
	}
	return at
}

func codeModeTrivia(source string, at int) int {
	for at < len(source) {
		next := codeModeSpace(source, at)
		if comment := codeModeSkipComment(source, next); comment > next {
			at = comment
			continue
		}
		return next
	}
	return at
}

func codeModeSkipLiteral(source string, at int) int {
	if next := codeModeSkipComment(source, at); next > at {
		return next
	}
	return codeModeSkipString(source, at)
}

func codeModeSkipComment(source string, at int) int {
	if at+1 >= len(source) || source[at] != '/' {
		return at
	}
	if source[at+1] == '/' {
		if end := strings.IndexByte(source[at+2:], '\n'); end >= 0 {
			return at + 2 + end + 1
		}
		return len(source)
	}
	if source[at+1] == '*' {
		if end := strings.Index(source[at+2:], "*/"); end >= 0 {
			return at + 2 + end + 2
		}
		return len(source)
	}
	return at
}

func codeModeSkipString(source string, at int) int {
	if at >= len(source) || source[at] != '"' && source[at] != '\'' && source[at] != '`' {
		return at
	}
	quote := source[at]
	for next := at + 1; next < len(source); next++ {
		if source[next] == '\\' {
			next++
		} else if source[next] == quote {
			return next + 1
		} else if quote != '`' && (source[next] == '\r' || source[next] == '\n') {
			return next
		}
	}
	return len(source)
}
