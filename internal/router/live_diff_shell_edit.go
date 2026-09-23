package router

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// liveDiffShellStatements parses display-only stock command input. It never
// expands shell values or authorizes execution. Recovery is limited to the
// parser's incomplete final construct so streaming cat and interpreter
// heredocs can become visible before the host completes the call.
func liveDiffShellStatements(input, directory string) ([]*syntax.Stmt, string, bool, bool) {
	header, err := shellsyntax.Parse(input)
	if err != nil || len(header.Interpreter) != 1 {
		return nil, "", false, false
	}
	variant := syntax.LangBash
	switch shellInterpreterName(header.Interpreter[0]) {
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
	return strings.TrimSuffix(input, "\n") + "\n" + delimiter + "\n", true
}

func liveDiffShellHeredoc(redirect *syntax.Redirect, partial bool) (string, bool) {
	if (redirect.Op != syntax.Hdoc && redirect.Op != syntax.DashHdoc) ||
		(redirect.N != nil && redirect.N.Value != "0") || redirect.Hdoc == nil {
		return "", false
	}
	var source strings.Builder
	for _, part := range redirect.Hdoc.Parts {
		literal, ok := part.(*syntax.Lit)
		if !ok {
			return "", false
		}
		source.WriteString(literal.Value)
	}
	content := source.String()
	quoted := false
	for _, part := range redirect.Word.Parts {
		switch part := part.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			quoted = true
		case *syntax.Lit:
			quoted = quoted || strings.Contains(part.Value, `\`)
		}
	}
	if !quoted {
		var err error
		content, err = expand.Document(&expand.Config{}, redirect.Hdoc)
		if err != nil {
			return "", false
		}
	}
	if redirect.Op == syntax.DashHdoc {
		lines := strings.Split(content, "\n")
		for index := range lines {
			lines[index] = strings.TrimLeft(lines[index], "\t")
		}
		content = strings.Join(lines, "\n")
	}
	if partial {
		content = strings.TrimSuffix(content, "\n")
	}
	return content, true
}

func shellInterpreterName(interpreter string) string {
	return shellsyntax.InterpreterIdentity(interpreter)
}

func shellCatLiteral(word *syntax.Word) (string, bool) {
	if word == nil || !shellCatLiteralParts(word.Parts, false) {
		return "", false
	}
	value, err := expand.Literal(&expand.Config{}, word)
	return value, err == nil
}

func shellCatLiteralParts(parts []syntax.WordPart, quoted bool) bool {
	for _, part := range parts {
		switch value := part.(type) {
		case *syntax.SglQuoted:
		case *syntax.DblQuoted:
			if !shellCatLiteralParts(value.Parts, true) {
				return false
			}
		case *syntax.Lit:
			if !quoted && strings.ContainsAny(value.Value, "~*?[") {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func shellFilePath(directory, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return directory + string(os.PathSeparator) + path
}

func liveDiffShellPreviewNeutral(stmt *syntax.Stmt) bool {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Coprocess || stmt.Disown || stmt.Negated ||
		len(stmt.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) == 0 {
		return false
	}
	name, literal := shellCatLiteral(call.Args[0])
	if !literal {
		return false
	}
	switch name {
	case "mkdir", "mread", "mcat", "msymbol", "inspect_file", "mchanges":
	default:
		return false
	}
	for _, argument := range call.Args[1:] {
		if _, literal := shellCatLiteral(argument); !literal {
			return false
		}
	}
	return true
}
