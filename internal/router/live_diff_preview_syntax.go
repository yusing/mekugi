package router

import (
	"context"
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

// Display-only syntax boundaries survive transport clipping and never select
// an interpreter for execution. Offsets are bytes in the displayed Input.
type liveDiffSourceSpan struct {
	Offset int
	Path   string
}

// Keep language selection outside the bounded painting window, so scrolling
// past a selector does not silently turn Python or JavaScript back into Bash.
func liveDiffScriptSyntax(input string) []liveDiffSourceSpan {
	var spans []liveDiffSourceSpan
	offset := 0

	for _, program := range shellsyntax.SplitSource(input) {
		path := "stream.sh"
		if parsed, err := shellsyntax.Parse(program); err == nil {
			args := parsed.Interpreter
			if shellsyntax.InterpreterIdentity(args[0]) == "uv" && len(args) >= 3 && args[1] == "run" {
				args = args[2:]
			}
			path = liveDiffLanguagePath(toolActivityLanguage(args[0]))
		}
		spans = append(spans, liveDiffSourceSpan{offset, path})
		if path == "stream.sh" {
			for _, span := range liveDiffInlineHeredocSyntax(program) {
				span.Offset += offset
				spans = append(spans, span)
			}
		}
		offset += len(program)
	}
	return spans
}

// A shell batch can contain a literal interpreter heredoc after other
// commands. Keep the shell framing but paint only its body as program source.
func liveDiffInlineHeredocSyntax(program string) []liveDiffSourceSpan {
	if !strings.Contains(program, "<<") {
		return nil
	}
	parsed, err := shellsyntax.Parse(program)
	if err != nil || len(parsed.Interpreter) != 1 {
		return nil
	}
	interpreter := shellsyntax.InterpreterIdentity(parsed.Interpreter[0])
	if interpreter != "bash" && interpreter != "sh" {
		return nil
	}
	base := strings.Index(program, parsed.Body)
	if base < 0 {
		return nil
	}
	statements, _, _, ok := liveDiffShellStatements(program, "")
	if !ok {
		return nil
	}
	var spans []liveDiffSourceSpan
	var visit func(*syntax.Stmt)
	visit = func(statement *syntax.Stmt) {
		if binary, ok := statement.Cmd.(*syntax.BinaryCmd); ok {
			visit(binary.X)
			visit(binary.Y)
			return
		}
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 || len(statement.Redirs) != 1 {
			return
		}
		command, literal := shellCatLiteral(call.Args[0])
		name := shellsyntax.InterpreterIdentity(command)
		if !literal || !shellInterpreterPattern.MatchString(name) {
			return
		}
		redirect := statement.Redirs[0]
		if redirect.Hdoc == nil || (redirect.Op != syntax.Hdoc && redirect.Op != syntax.DashHdoc) {
			return
		}
		delimiter, literal := shellCatLiteral(redirect.Word)
		if !literal {
			return
		}
		start := base + int(redirect.Hdoc.Pos().Offset())
		end := base + int(redirect.Hdoc.End().Offset()) - len(delimiter)
		if start < 0 || start >= len(program) || end <= start {
			return
		}
		spans = append(spans, liveDiffSourceSpan{start, liveDiffLanguagePath(toolActivityLanguage(name))})
		if end < len(program) {
			spans = append(spans, liveDiffSourceSpan{end, "stream.sh"})
		}
	}
	for _, statement := range statements {
		visit(statement)
	}
	return spans
}

func liveDiffLanguagePath(language string) string {
	switch language {
	case "python":
		return "stream.py"
	case "javascript", "typescript":
		return "stream.ts"
	case "ruby":
		return "stream.rb"
	case "perl":
		return "stream.pl"
	case "php":
		return "stream.php"
	case "lua":
		return "stream.lua"
	case "tcl":
		return "stream.tcl"
	case "r":
		return "stream.r"
	case "haskell":
		return "stream.hs"
	case "awk":
		return "stream.awk"
	case "powershell":
		return "stream.ps1"
	case "sql":
		return "stream.sql"
	default:
		return "stream.sh"
	}
}

// Clip offsets together with source, retaining the language of a removed header.
func liveDiffClipSyntax(spans []liveDiffSourceSpan, cut int) []liveDiffSourceSpan {
	var clipped []liveDiffSourceSpan
	for _, span := range spans {
		if span.Offset <= cut {
			clipped = []liveDiffSourceSpan{{Path: span.Path}}
		} else {
			clipped = append(clipped, liveDiffSourceSpan{span.Offset - cut, span.Path})
		}
	}
	return clipped
}

func liveDiffSourceRows(input string, spans []liveDiffSourceSpan) []liveDiffSourceSpan {
	var rows []liveDiffSourceSpan
	offset, index := 0, 0
	for line := range strings.SplitSeq(strings.TrimSuffix(input, "\n"), "\n") {
		for index+1 < len(spans) && spans[index+1].Offset <= offset {
			index++
		}
		rows = append(rows, spans[index])
		offset += len(line) + 1
	}
	return rows
}

func (p *liveDiffPreviewView) colorScript(ctx context.Context, theme liveDiffTheme, start, end int) ([]string, error) {
	var lines []string
	for at := start; at < end; {
		span := p.paths[at]
		next := at
		var source strings.Builder
		for next < end && p.paths[next] == span {
			source.WriteString(livediff.Safe(strings.TrimSuffix(p.source[next].text, "\n"), false))
			source.WriteByte('\n')
			next++
		}
		colored, err := p.renderer.ColorSource(ctx, theme, span.Path, source.String())
		if err != nil {
			return nil, err
		}
		lines = append(lines, colored...)
		at = next
	}
	return lines, nil
}
