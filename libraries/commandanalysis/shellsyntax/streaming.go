package shellsyntax

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// StreamingStatements parses display-only stock command input. It never
// expands shell values or authorizes execution. Recovery is limited to the
// parser's incomplete final construct so streaming cat and interpreter
// heredocs can become visible before the host completes the call.
func StreamingStatements(input, directory string) ([]*syntax.Stmt, string, bool, bool) {
	header, err := Parse(input)
	if err != nil || len(header.Interpreter) != 1 {
		return nil, "", false, false
	}
	variant := syntax.LangBash
	switch InterpreterIdentity(header.Interpreter[0]) {
	case "bash":
	case "sh":
		variant = syntax.LangPOSIX
	default:
		return nil, "", false, false
	}
	if value, exists := header.Params["workdir"]; exists {
		workdir, ok := value.(string)
		if !ok || !filepath.IsAbs(workdir) {
			return nil, "", false, false
		}
		directory = workdir
	}
	program, parseErr := syntax.NewParser(syntax.Variant(variant)).Parse(strings.NewReader(header.Body), "")
	partial := false
	if parseErr != nil {
		partial = true
		if completed, ok := completeStreamingHeredoc(header.Body); ok {
			program, _ = syntax.NewParser(syntax.Variant(variant)).Parse(strings.NewReader(completed), "")
		} else {
			program, _ = syntax.NewParser(syntax.Variant(variant), syntax.RecoverErrors(4)).Parse(strings.NewReader(header.Body), "")
		}
	}
	if program == nil || len(program.Stmts) == 0 {
		return nil, "", false, false
	}
	return program.Stmts, directory, partial, true
}

// Sequence flattens `a && b` chains into their steps.
func Sequence(statements []*syntax.Stmt) []*syntax.Stmt {
	var sequence []*syntax.Stmt
	var add func(*syntax.Stmt)
	add = func(stmt *syntax.Stmt) {
		binary, ok := stmt.Cmd.(*syntax.BinaryCmd)
		if ok && binary.Op == syntax.AndStmt && !stmt.Background && !stmt.Coprocess && !stmt.Disown && !stmt.Negated && len(stmt.Redirs) == 0 {
			add(binary.X)
			add(binary.Y)
			return
		}
		sequence = append(sequence, stmt)
	}
	for _, stmt := range statements {
		add(stmt)
	}
	return sequence
}

// LiteralCd returns the directory a literal `cd` selects for later
// steps. Home-relative, previous-directory, and computed targets do not match.
func LiteralCd(stmt *syntax.Stmt, directory string) (string, bool) {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Coprocess || stmt.Disown || stmt.Negated ||
		len(stmt.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) != 2 {
		return "", false
	}
	name, literal := Literal(call.Args[0])
	target, targetLiteral := Literal(call.Args[1])
	if !literal || name != "cd" || !targetLiteral || target == "" || target == "-" || strings.HasPrefix(target, "~") {
		return "", false
	}
	if !filepath.IsAbs(target) {
		if !filepath.IsAbs(directory) {
			return "", false
		}
		target = filepath.Join(directory, target)
	}
	return filepath.Clean(target), true
}

var streamingHeredocOpener = regexp.MustCompile(`<<(-?)[ \t]*(?:'([A-Za-z_][A-Za-z0-9_]*)'|"([A-Za-z_][A-Za-z0-9_]*)"|([A-Za-z_][A-Za-z0-9_]*))`)

func completeStreamingHeredoc(input string) (string, bool) {
	matches := streamingHeredocOpener.FindAllStringSubmatchIndex(input, -1)
	if len(matches) == 0 {
		return "", false
	}
	match := matches[len(matches)-1]
	delimiter := ""
	for _, pair := range [][2]int{{match[4], match[5]}, {match[6], match[7]}, {match[8], match[9]}} {
		if pair[0] >= 0 {
			delimiter = input[pair[0]:pair[1]]
			break
		}
	}
	if delimiter == "" {
		return "", false
	}
	stripTabs := match[2] >= 0 && input[match[2]:match[3]] == "-"
	lineEnd := strings.IndexByte(input[match[1]:], '\n')
	if lineEnd < 0 {
		return "", false
	}
	body := input[match[1]+lineEnd+1:]
	for line := range strings.SplitSeq(body, "\n") {
		if stripTabs {
			line = strings.TrimLeft(line, "\t")
		}
		if line == delimiter {
			return "", false
		}
	}
	// Always add a synthetic newline. The heredoc reader removes exactly
	// this newline, preserving a real received newline at the source tip.
	return input + "\n" + delimiter + "\n", true
}

func FilePath(directory, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return directory + string(os.PathSeparator) + path
}
